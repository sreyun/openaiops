/* ===================== 自动化运维：剧本编排 + 批量执行 ===================== */
let PB_HOSTS = []; // cached full host list for target selection
let PB_CATS = []; // cached unique categories

async function loadPlaybooks() {
  const list = $("playbookList"), empty = $("playbookEmpty");
  if (list) list.innerHTML = `<div class="empty-line">${I18N.t("common.loading","加载中…")}</div>`;
  if (empty) empty.style.display = "none";
  try {
    if (typeof loadHostFolders === "function") {
      try { await loadHostFolders(); } catch (_) {}
    }
    const [pbs, hosts] = await Promise.all([
      fetch(`${API}/playbooks`, { credentials: "same-origin" }).then(r => r.json()),
      typeof fetchHostsList === "function"
        ? fetchHostsList({ force: true })
        : fetch(`${API}/hosts`, { credentials: "same-origin" }).then(r => r.json()).then(j =>
            typeof syncHostCache === "function" ? syncHostCache(j) : (Array.isArray(j) ? j : []))
    ]);
    PB_HOSTS = Array.isArray(hosts) ? hosts : [];
    // Extract unique categories for legacy category: targets / fallback UI
    PB_CATS = [...new Set(PB_HOSTS.map(h => h.category || I18N.t("section.uncategorized")))].sort();
    // System types are hardcoded (linux/macos/windows) — do NOT extract from
    // h.platform (which is a version string like "Ubuntu 22.04"), use h.os
    // (runtime.GOOS: "linux"/"windows"/"darwin") for matching.
    LAST_PLAYBOOKS = pbs || [];
    renderPlaybooks(LAST_PLAYBOOKS);
  } catch (e) {
    console.warn("load playbooks:", e);
    if (list) list.innerHTML = `<div class="empty-line">${I18N.t("sre.load_failed","加载失败")}</div>`;
  }
}

function switchAutomationView(mode) {
  AUTOMATION_VIEW_MODE = mode;
  try { localStorage.setItem("aiops_pb_view", mode); } catch (e) {}
  loadPlaybooks(); // 重新拉取并渲染（renderPlaybooks 内按模式设 className + 同步按钮态）
}

function renderPlaybooks(pbs) {
  const list = $("playbookList"), empty = $("playbookEmpty");
  if (!list) return;
  if (PB_SEARCH) {
    pbs = (pbs || []).filter(p => matchesSearchTokens(
      [p.name, p.description, p.id].filter(Boolean).join(" "),
      PB_SEARCH
    ));
  }
  if (empty) empty.style.display = pbs.length === 0 ? "" : "none";
  // 视图模式：卡片(默认) / 列表——复用同一 .pb-card 结构，列表态仅由 CSS 重排为紧凑单行，
  // 从而不改动 data-pbact 委托对 .pb-card[data-id] 的依赖。
  list.className = AUTOMATION_VIEW_MODE === "list" ? "pb-listmode" : "";
  const vt = $("playbookViewToggle");
  if (vt) vt.querySelectorAll(".vt-btn").forEach(b => b.classList.toggle("active", b.dataset.view === AUTOMATION_VIEW_MODE));
  list.innerHTML = pbs.map(pb => {
    const stepCount = (pb.steps || []).length;
    const targets = [...new Set((pb.steps || []).map(s => s.target))];
    const sched = pb.schedule && pb.schedule.enabled;
    return `<div class="pb-card" data-id="${esc(pb.id)}">
      <div class="pb-card-top">
        <div class="pb-card-title">
          <strong>${esc(pb.name)}</strong>
          ${pb.description ? `<span class="pb-desc">${esc(pb.description)}</span>` : `<span class="pb-desc pb-desc-empty">${I18N.t("sre.pb_no_desc","暂无描述")}</span>`}
        </div>
        ${sched ? `<span class="pb-sched-badge" title="${I18N.t("playbook.sched_badge_title")}">⏱ ${esc(pbSchedLabel(pb.schedule))}</span>` : ""}
      </div>
      <div class="pb-card-foot">
        <div class="pb-pills">
          <span class="pb-pill">${stepCount} ${I18N.t("sre.unit_steps","步骤")}</span>
          <span class="pb-pill">${targets.length} ${I18N.t("sre.unit_targets","目标")}</span>
          <span class="pb-pill pb-pill-id mono">${esc(pb.id)}</span>
        </div>
        <div class="pb-actions">
          <button class="btn primary sm" data-pbact="exec">▶ ${I18N.t("ui.execute")}</button>
          <button class="btn sm" data-pbact="edit">${I18N.t("ui.edit")}</button>
          <button class="btn danger sm" data-pbact="del">${I18N.t("ui.delete")}</button>
        </div>
      </div>
    </div>`;
  }).join("");
}

function openPlaybookModal(pb) {
  $("playbookModalTitle").textContent = pb ? I18N.t("ui.edit_playbook") : I18N.t("ui.new_playbook");
  $("pbId").value = pb ? pb.id : "";
  $("pbName").value = pb ? pb.name : "";
  $("pbDesc").value = pb ? (pb.description || "") : "";
  const strategy = (pb && pb.strategy) || {};
  $("pbMaxParallel").value = strategy.max_parallel || 30;
  $("pbAutoRollback").checked = !!strategy.auto_rollback;
  const steps = pb ? pb.steps : [];
  renderPbSteps(steps.length > 0 ? steps : [{
    name: "系统信息", module: "gather_facts", target: "", timeout_sec: 30,
    continue_on_error: false, register: "facts"
  }]);
  // Populate the timed-trigger fields from the playbook's schedule (if any).
  const sc = (pb && pb.schedule) ? pb.schedule : null;
  $("pbSchedEnabled").checked = !!(sc && sc.enabled);
  $("pbSchedKind").value = (sc && sc.kind) || "interval";
  $("pbSchedInterval").value = (sc && sc.interval_min) || 60;
  $("pbSchedAt").value = (sc && sc.at) || "03:00";
  $("pbSchedWeekday").value = String((sc && typeof sc.weekday === "number") ? sc.weekday : 1);
  pbSchedRefresh();
  $("playbookMask").classList.add("show");
  loadPlaybookRevisions(pb && pb.id);
}
async function loadPlaybookRevisions(id) {
  const panel = $("pbRevPanel"), list = $("pbRevList");
  if (!panel || !list) return;
  if (!id) { panel.style.display = "none"; return; }
  panel.style.display = "";
  list.textContent = "加载中…";
  try {
    const j = await fetch(`${API}/playbooks/${encodeURIComponent(id)}/revisions`).then(r => r.json());
    const revs = j.revisions || [];
    if (!revs.length) { list.textContent = "尚无版本快照（保存后生成）"; return; }
    list.innerHTML = revs.map(r =>
      `<div class="sre-row" style="padding:6px 0"><div class="sre-row-main"><div class="mono">rev ${r.rev} · ${esc(r.name||"")} · ${r.steps||0} 步</div>
        <div class="sre-row-sub">${fmtDateTime(r.saved_at)} · ${esc(r.actor||"")}</div></div>
        <button class="btn sm" type="button" data-pb-restore="${r.rev}">还原</button></div>`
    ).join("");
    list.querySelectorAll("[data-pb-restore]").forEach(btn => {
      btn.onclick = async () => {
        const ok = typeof uiConfirm === "function"
          ? await uiConfirm({ title: "还原版本", message: `确认还原到 rev ${btn.dataset.pbRestore}？当前内容将被覆盖并生成新版本。`, tone: "warn" })
          : confirm(`确认还原到 rev ${btn.dataset.pbRestore}？当前内容将被覆盖并生成新版本。`);
        if (!ok) return;
        const r = await fetch(`${API}/playbooks/${encodeURIComponent(id)}/revisions/${btn.dataset.pbRestore}/restore`, { method: "POST" });
        const j = await r.json().catch(()=>({}));
        if (r.ok) { toast("已还原版本", "ok"); loadPlaybooks(); $("playbookMask").classList.remove("show"); }
        else toast(j.error || "还原失败", "err");
      };
    });
  } catch (e) { list.textContent = "版本历史加载失败"; }
}

// Show/hide the schedule sub-fields based on the enable toggle + selected kind.
function pbSchedRefresh() {
  const on = $("pbSchedEnabled").checked;
  $("pbSchedFields").style.display = on ? "" : "none";
  const kind = $("pbSchedKind").value;
  $("pbSchedIntervalField").style.display = (kind === "interval") ? "" : "none";
  $("pbSchedAtField").style.display = (kind === "daily" || kind === "weekly") ? "" : "none";
  $("pbSchedWeekdayField").style.display = (kind === "weekly") ? "" : "none";
}

// Human-readable schedule summary for the playbook card badge.
function pbSchedLabel(sc) {
  if (!sc || !sc.enabled) return "";
  if (sc.kind === "interval") return `${I18N.t("sre.sched_every","每")} ${sc.interval_min} ${I18N.t("sre.unit_minutes","分钟")}`;
  if (sc.kind === "daily") return `${I18N.t("sre.sched_daily","每天")} ${sc.at}`;
  if (sc.kind === "weekly") { const wd = [I18N.t("sre.wd_0","日"),I18N.t("sre.wd_1","一"),I18N.t("sre.wd_2","二"),I18N.t("sre.wd_3","三"),I18N.t("sre.wd_4","四"),I18N.t("sre.wd_5","五"),I18N.t("sre.wd_6","六")][sc.weekday] || ""; return `${I18N.t("sre.sched_weekly","每周")}${wd} ${sc.at}`; }
  return I18N.t("sre.sched_scheduled","定时");
}

function renderPbSteps(steps) {
  const c = $("pbSteps");
  c.innerHTML = steps.map((s, i) => {
    const a = s.args || {};
    const mod = s.module || "";
    const av = (k) => esc(a[k] || "");
    const optSel = (v, cur) => (v === (cur || "") ? "selected" : "");
    return `<div class="pb-step" data-idx="${i}">
      <div class="grid2">
        <div class="field"><label>${I18N.t("form.step_name")}</label><input type="text" class="pb-step-name" value="${esc(s.name||"")}" placeholder="${I18N.t('form.hint_step_name')}"></div>
        <div class="field pb-target-field"><label>${I18N.t("form.target")}</label>
          <input type="hidden" class="pb-step-target" value="${esc(s.target || "")}">
          <div class="pb-step-target-picker"></div>
        </div>
      </div>
      <div class="pb-target-preview" style="font-size:12px;color:var(--muted2);margin:-4px 0 4px"></div>
      <div class="field"><label>${I18N.t("sre.label_type","类型")}</label><div class="select-wrap"><select class="pb-step-module" data-act-change="pb-module-change">
        <option value="" ${optSel("",mod)}>${I18N.t("sre.mod_shell","Shell 命令")}</option>
        <optgroup label="${I18N.t("sre.mod_g_system","系统运维 · 只读")}">
          <option value="gather_facts" ${optSel("gather_facts",mod)}>采集主机信息 · gather_facts</option>
          <option value="host_inspect" ${optSel("host_inspect",mod)}>深度主机巡检 · host_inspect</option>
          <option value="disk_usage" ${optSel("disk_usage",mod)}>磁盘用量 · disk_usage</option>
          <option value="mem_info" ${optSel("mem_info",mod)}>内存概况 · mem_info</option>
          <option value="cpu_load" ${optSel("cpu_load",mod)}>CPU/负载 · cpu_load</option>
          <option value="process_top" ${optSel("process_top",mod)}>进程占用 · process_top</option>
          <option value="uptime_info" ${optSel("uptime_info",mod)}>运行时长 · uptime_info</option>
          <option value="pkg_list" ${optSel("pkg_list",mod)}>已装软件包 · pkg_list</option>
          <option value="service_status" ${optSel("service_status",mod)}>服务状态查询 · service_status</option>
          <option value="file_stat" ${optSel("file_stat",mod)}>文件元数据 · file_stat</option>
          <option value="file_head" ${optSel("file_head",mod)}>读文件开头 · file_head</option>
        </optgroup>
        <optgroup label="${I18N.t("sre.mod_g_network","网络运维 · 只读")}">
          <option value="net_ifaces" ${optSel("net_ifaces",mod)}>网卡地址 · net_ifaces</option>
          <option value="net_listen" ${optSel("net_listen",mod)}>监听端口 · net_listen</option>
          <option value="net_routes" ${optSel("net_routes",mod)}>路由表 · net_routes</option>
          <option value="net_sockets" ${optSel("net_sockets",mod)}>连接摘要 · net_sockets</option>
          <option value="dns_resolve" ${optSel("dns_resolve",mod)}>DNS 解析 · dns_resolve</option>
        </optgroup>
        <optgroup label="${I18N.t("sre.mod_g_sre","SRE / 可观测 · 只读")}">
          <option value="journal_recent" ${optSel("journal_recent",mod)}>最近系统日志 · journal_recent</option>
          <option value="dmesg_recent" ${optSel("dmesg_recent",mod)}>内核消息 · dmesg_recent</option>
          <option value="docker_ps" ${optSel("docker_ps",mod)}>容器列表 · docker_ps</option>
          <option value="docker_stats" ${optSel("docker_stats",mod)}>容器资源 · docker_stats</option>
          <option value="container_logs" ${optSel("container_logs",mod)}>容器日志 · container_logs</option>
          <option value="container_compose_ls" ${optSel("container_compose_ls",mod)}>Compose 项目 · container_compose_ls</option>
          <option value="kube_get" ${optSel("kube_get",mod)}>K8s 资源 · kube_get</option>
          <option value="time_sync" ${optSel("time_sync",mod)}>时间/时区 · time_sync</option>
        </optgroup>
        <optgroup label="${I18N.t("sre.mod_g_security","安全运维 · 只读")}">
          <option value="users_logged" ${optSel("users_logged",mod)}>登录会话 · users_logged</option>
          <option value="security_listen" ${optSel("security_listen",mod)}>对外监听 · security_listen</option>
          <option value="auth_failures" ${optSel("auth_failures",mod)}>认证失败摘要 · auth_failures</option>
          <option value="host_security_scan" ${optSel("host_security_scan",mod)}>主机安全扫描 · host_security_scan</option>
        </optgroup>
        <optgroup label="${I18N.t("sre.mod_g_bigdata","大数据运维 · 只读")}">
          <option value="bigdata_jps" ${optSel("bigdata_jps",mod)}>Java 进程 · bigdata_jps</option>
          <option value="bigdata_ports" ${optSel("bigdata_ports",mod)}>大数据端口 · bigdata_ports</option>
        </optgroup>
        <optgroup label="${I18N.t("sre.mod_g_change","变更操作 · 会修改系统")}">
          <option value="service" ${optSel("service",mod)}>服务启停 · service</option>
          <option value="package" ${optSel("package",mod)}>软件包 · package</option>
          <option value="copy" ${optSel("copy",mod)}>写入文件 · copy</option>
          <option value="container_action" ${optSel("container_action",mod)}>容器启停 · container_action</option>
          <option value="container_compose" ${optSel("container_compose",mod)}>Compose 操作 · container_compose</option>
          <option value="hyperv_power" ${optSel("hyperv_power",mod)}>Hyper-V 电源 · hyperv_power</option>
          <option value="hyperv_set" ${optSel("hyperv_set",mod)}>Hyper-V 配置 · hyperv_set</option>
        </optgroup>
      </select></div></div>

      <div class="pb-mod pb-mod-shell" style="display:none">
        <div class="field"><label>${I18N.t("form.command")}</label><textarea class="pb-step-cmd" rows="2" placeholder="${I18N.t('form.hint_command')}" spellcheck="false" style="resize:vertical;min-height:54px;line-height:1.5">${esc(s.command||"")}</textarea></div>
        <details class="pb-adv"${(s.command_win||s.command_mac)?" open":""}><summary style="cursor:pointer;font-size:12px;color:var(--muted2);margin:2px 0 6px">${I18N.t("sre.pb_per_os_cmd","分系统命令（留空则统一用上面的命令）")}</summary>
          <div class="field"><label>${I18N.t("sre.pb_win_override","Windows 覆盖命令")}</label><textarea class="pb-step-cmdwin" rows="2" spellcheck="false" style="resize:vertical;min-height:44px" placeholder="${I18N.t("sre.pb_win_override_ph","仅 Windows 主机执行此命令")}">${esc(s.command_win||"")}</textarea></div>
          <div class="field"><label>${I18N.t("sre.pb_mac_override","macOS 覆盖命令")}</label><textarea class="pb-step-cmdmac" rows="2" spellcheck="false" style="resize:vertical;min-height:44px" placeholder="${I18N.t("sre.pb_mac_override_ph","仅 macOS 主机执行此命令")}">${esc(s.command_mac||"")}</textarea></div>
        </details>
      </div>

      <div class="pb-mod pb-mod-gather_facts" style="display:none">
        <div class="pb-mod-hint">${I18N.t("sre.pb_gather_desc","采集主机名、IP、架构、CPU、内存摘要与负载（跨系统一致，只读）。建议「保存输出到变量」。")}</div>
      </div>
      <div class="pb-mod pb-mod-host_inspect" style="display:none">
        <div class="pb-mod-hint">${I18N.t("sre.pb_host_inspect_desc","深度主机巡检（Windows/Linux/macOS/麒麟），输出结构化 JSON 报告。告警级发现会以非零退出码返回，模板默认「忽略非零退出码」以免阻断后续步骤；完整 Web 报告也可在「主机巡检」Tab 查看。")}</div>
        <div class="field"><label>${I18N.t("sre.pb_inspect_profile","巡检档位 profile")}</label><div class="select-wrap"><select class="pb-arg-inspect-profile">
          <option value="quick" ${optSel("quick", a.profile||"standard")}>${I18N.t("sre.pb_inspect_quick","快速")} quick</option>
          <option value="standard" ${optSel("standard", a.profile||"standard")}>${I18N.t("sre.pb_inspect_standard","标准")} standard</option>
          <option value="deep" ${optSel("deep", a.profile||"standard")}>${I18N.t("sre.pb_inspect_deep","深度")} deep</option>
        </select></div></div>
      </div>
      <div class="pb-mod pb-mod-disk_usage pb-mod-mem_info pb-mod-cpu_load pb-mod-process_top pb-mod-uptime_info pb-mod-pkg_list pb-mod-net_ifaces pb-mod-net_listen pb-mod-net_routes pb-mod-net_sockets pb-mod-docker_ps pb-mod-docker_stats pb-mod-container_compose_ls pb-mod-dmesg_recent pb-mod-time_sync pb-mod-users_logged pb-mod-security_listen pb-mod-auth_failures pb-mod-host_security_scan pb-mod-bigdata_jps pb-mod-bigdata_ports" style="display:none">
        <div class="pb-mod-hint">${I18N.t("sre.pb_readonly_hint","只读采集模块：不会修改系统配置、不会启停服务、不会写入文件。")}</div>
      </div>
      <div class="pb-mod pb-mod-container_logs" style="display:none">
        <div class="pb-mod-hint">只读拉取容器日志（docker/podman logs）。</div>
        <div class="grid2">
          <div class="field"><label>容器 id/name</label><input type="text" class="pb-arg-ctr-id" value="${av('id')||av('name')}" placeholder="my-app"></div>
          <div class="field"><label>tail 行数</label><input type="text" class="pb-arg-ctr-tail mono" value="${av('tail')||'100'}" style="width:100px"></div>
        </div>
      </div>
      <div class="pb-mod pb-mod-container_action" style="display:none">
        <div class="pb-mod-hint" style="color:var(--warn-txt)">⚠ 变更类：启停/重启容器。</div>
        <div class="grid2">
          <div class="field"><label>容器 id/name</label><input type="text" class="pb-arg-ctr-act-id" value="${av('id')||av('name')}" placeholder="my-app"></div>
          <div class="field"><label>action</label><div class="select-wrap"><select class="pb-arg-ctr-act">
            <option value="restart" ${optSel('restart',a.action||'restart')}>restart</option>
            <option value="start" ${optSel('start',a.action)}>start</option>
            <option value="stop" ${optSel('stop',a.action)}>stop</option>
          </select></div></div>
        </div>
      </div>
      <div class="pb-mod pb-mod-container_compose" style="display:none">
        <div class="pb-mod-hint" style="color:var(--warn-txt)">⚠ Compose up/down/pull 等会变更运行态。</div>
        <div class="grid2">
          <div class="field"><label>project / file</label><input type="text" class="pb-arg-compose-project" value="${av('project')||av('file')}" placeholder="myproject 或 /path/compose.yml"></div>
          <div class="field"><label>action</label><div class="select-wrap"><select class="pb-arg-compose-act">
            <option value="ps" ${optSel('ps',a.action||'ps')}>ps</option>
            <option value="up" ${optSel('up',a.action)}>up</option>
            <option value="down" ${optSel('down',a.action)}>down</option>
            <option value="pull" ${optSel('pull',a.action)}>pull</option>
            <option value="logs" ${optSel('logs',a.action)}>logs</option>
          </select></div></div>
        </div>
      </div>
      <div class="pb-mod pb-mod-hyperv_power" style="display:none">
        <div class="pb-mod-hint" style="color:var(--warn-txt)">⚠ Hyper-V 电源操作（建议 when: {{os}} == windows）。</div>
        <div class="grid2">
          <div class="field"><label>vm_id / name</label><input type="text" class="pb-arg-hv-name" value="${av('vm_id')||av('name')}" placeholder="VM 名称或 GUID"></div>
          <div class="field"><label>action</label><div class="select-wrap"><select class="pb-arg-hv-power">
            <option value="start" ${optSel('start',a.action||'start')}>start</option>
            <option value="stop" ${optSel('stop',a.action)}>stop</option>
            <option value="restart" ${optSel('restart',a.action)}>restart</option>
            <option value="force_stop" ${optSel('force_stop',a.action)}>force_stop</option>
          </select></div></div>
        </div>
      </div>
      <div class="pb-mod pb-mod-hyperv_set" style="display:none">
        <div class="pb-mod-hint" style="color:var(--warn-txt)">⚠ 调整 Hyper-V CPU/内存（虚拟机宜关机）。</div>
        <div class="field"><label>vm_id / name</label><input type="text" class="pb-arg-hvset-name" value="${av('vm_id')||av('name')}" placeholder="VM 名称或 GUID"></div>
        <div class="grid2">
          <div class="field"><label>processor_count</label><input type="text" class="pb-arg-hvset-cpu mono" value="${av('processor_count')}" placeholder="4"></div>
          <div class="field"><label>memory_mb</label><input type="text" class="pb-arg-hvset-mem mono" value="${av('memory_mb')}" placeholder="4096"></div>
        </div>
      </div>
      <div class="pb-mod pb-mod-service_status" style="display:none">
        <div class="pb-mod-hint">只读查询服务状态（systemctl status / sc query），不会启停。</div>
        <div class="field"><label>${I18N.t("sre.label_service_name","服务名")}</label><input type="text" class="pb-arg-svcstatus-name" value="${av('name')}" placeholder="nginx"></div>
      </div>
      <div class="pb-mod pb-mod-file_stat pb-mod-file_head" style="display:none">
        <div class="pb-mod-hint">只读文件访问；敏感路径（shadow、.ssh 等）会被拦截。</div>
        <div class="field"><label>路径 path</label><input type="text" class="pb-arg-filepath" value="${av('path')}" placeholder="/var/log/messages"></div>
      </div>
      <div class="pb-mod pb-mod-dns_resolve" style="display:none">
        <div class="field"><label>主机名 host</label><input type="text" class="pb-arg-dns-host" value="${av('host')}" placeholder="example.com"></div>
      </div>
      <div class="pb-mod pb-mod-journal_recent" style="display:none">
        <div class="pb-mod-hint">只读拉取最近日志行。</div>
        <div class="field"><label>行数 lines</label><input type="text" class="pb-arg-journal-lines mono" value="${av('lines')||'80'}" style="width:100px"></div>
      </div>
      <div class="pb-mod pb-mod-kube_get" style="display:none">
        <div class="pb-mod-hint">只读 kubectl get（默认 pods -A）。</div>
        <div class="field"><label>resource</label><input type="text" class="pb-arg-kube-resource" value="${av('resource')||'pods'}" placeholder="pods"></div>
      </div>

      <div class="pb-mod pb-mod-service" style="display:none">
        <div class="pb-mod-hint" style="color:var(--warn-txt)">⚠ 变更类：会启停/重载服务，可能影响业务。</div>
        <div class="grid2">
          <div class="field"><label>${I18N.t("sre.label_service_name","服务名")}</label><input type="text" class="pb-arg-service-name" value="${av('name')}" placeholder="nginx"></div>
          <div class="field"><label>${I18N.t("sre.label_target_state","目标状态")}</label><div class="select-wrap"><select class="pb-arg-service-state">
            <option value="started" ${optSel('started',a.state)}>${I18N.t("sre.svc_started","启动")} started</option>
            <option value="stopped" ${optSel('stopped',a.state)}>${I18N.t("sre.svc_stopped","停止")} stopped</option>
            <option value="restarted" ${optSel('restarted',a.state)}>${I18N.t("sre.svc_restarted","重启")} restarted</option>
            <option value="reloaded" ${optSel('reloaded',a.state)}>${I18N.t("sre.svc_reloaded","重载")} reloaded</option>
          </select></div></div>
        </div>
        <div class="field"><label>${I18N.t("sre.label_boot_enable","开机自启")}</label><div class="select-wrap"><select class="pb-arg-service-enabled">
          <option value="" ${optSel('',a.enabled)}>${I18N.t("sre.opt_nochange","不修改")}</option>
          <option value="true" ${optSel('true',a.enabled)}>${I18N.t("sre.opt_enable","启用")}</option>
          <option value="false" ${optSel('false',a.enabled)}>${I18N.t("sre.opt_disable","禁用")}</option>
        </select></div></div>
      </div>

      <div class="pb-mod pb-mod-package" style="display:none">
        <div class="pb-mod-hint" style="color:var(--warn-txt)">⚠ 变更类：会安装/卸载软件包。</div>
        <div class="grid2">
          <div class="field"><label>${I18N.t("sre.label_pkg_name","包名")}</label><input type="text" class="pb-arg-package-name" value="${av('name')}" placeholder="nginx"></div>
          <div class="field"><label>${I18N.t("sre.label_action","操作")}</label><div class="select-wrap"><select class="pb-arg-package-state">
            <option value="present" ${optSel('present',a.state)}>${I18N.t("sre.pkg_install","安装")} present</option>
            <option value="absent" ${optSel('absent',a.state)}>${I18N.t("sre.pkg_remove","卸载")} absent</option>
            <option value="latest" ${optSel('latest',a.state)}>${I18N.t("sre.pkg_latest","安装/升级到最新")} latest</option>
          </select></div></div>
        </div>
        <div style="font-size:12px;color:var(--muted2);margin:2px 0 8px">${I18N.t("sre.pb_pkg_desc","自动探测系统包管理器（apt/dnf/yum/apk/zypper/pacman · brew · choco/winget）。")}</div>
      </div>

      <div class="pb-mod pb-mod-copy" style="display:none">
        <div class="pb-mod-hint" style="color:var(--warn-txt)">⚠ 变更类：会写入目标文件。</div>
        <div class="grid2">
          <div class="field"><label>${I18N.t("sre.label_dest_path","目标路径")}</label><input type="text" class="pb-arg-copy-dest" value="${av('dest')}" placeholder="/etc/app/config.yml"></div>
          <div class="field"><label>${I18N.t("sre.label_mode_octal","权限（八进制）")}</label><input type="text" class="pb-arg-copy-mode mono" value="${av('mode')}" placeholder="0644" style="width:110px"></div>
        </div>
        <div class="field"><label>${I18N.t("sre.label_file_content","文件内容")}</label><textarea class="pb-arg-copy-content" rows="4" spellcheck="false" style="resize:vertical;min-height:70px">${esc(a.content||"")}</textarea></div>
      </div>

      <details class="pb-adv"${(s.when||s.register)?" open":""}><summary style="cursor:pointer;font-size:12px;color:var(--muted2);margin:2px 0 6px">${I18N.t("sre.pb_cond_vars","条件与变量（选填）")}</summary>
        <div class="grid2">
          <div class="field"><label>${I18N.t("sre.label_when","when 条件")}</label><input type="text" class="pb-step-when" value="${esc(s.when||"")}" placeholder="${I18N.t("sre.pb_when_ph","{{os}} == linux | {{distro}} == rocky | {{distro}} == kylin | {{distro_version}} >= 10 | {{platform}} contains V11")}"></div>
          <div class="field"><label>${I18N.t("sre.label_register","保存输出到变量")}</label><input type="text" class="pb-step-register" value="${esc(s.register||"")}" placeholder="${I18N.t("sre.pb_register_ph","变量名 → 后续步骤用 {{变量名}} 引用")}"></div>
        </div>
      </details>

      <details class="pb-adv"${(s.rollback||s.rollback_win||s.rollback_mac||s.retry_on_exit)?" open":""}><summary style="cursor:pointer;font-size:12px;color:var(--muted2);margin:2px 0 6px">可靠性控制（重试与显式回滚）</summary>
        <div class="grid2">
          <div class="field"><label>最大尝试次数（1-6）</label><input type="number" class="pb-step-attempts mono" value="${s.max_attempts||3}" min="1" max="6"></div>
          <div class="field"><label>重试退避基数（秒）</label><input type="number" class="pb-step-retry-delay mono" value="${s.retry_delay_sec||2}" min="1" max="60"></div>
        </div>
        <label class="switch" style="display:flex;margin:2px 0 8px"><input type="checkbox" class="pb-step-retry-exit" ${s.retry_on_exit?"checked":""}> 非零退出码也重试（仅适用于幂等步骤）</label>
        <div class="field"><label>Linux / 默认回滚命令</label><textarea class="pb-step-rollback" rows="2" spellcheck="false" placeholder="仅在本步成功、后续步骤失败且剧本开启自动回滚时执行">${esc(s.rollback||"")}</textarea></div>
        <div class="grid2">
          <div class="field"><label>Windows 回滚覆盖</label><textarea class="pb-step-rollback-win" rows="2" spellcheck="false">${esc(s.rollback_win||"")}</textarea></div>
          <div class="field"><label>macOS 回滚覆盖</label><textarea class="pb-step-rollback-mac" rows="2" spellcheck="false">${esc(s.rollback_mac||"")}</textarea></div>
        </div>
      </details>

      <div class="grid2">
        <div class="field"><label>${I18N.t("form.timeout")}</label><input type="text" class="pb-step-timeout mono" value="${s.timeout_sec||30}" style="width:80px"></div>
        <div class="field"><label>${I18N.t("form.continue_err")}</label><label class="switch"><input type="checkbox" class="pb-step-cont" ${s.continue_on_error?"checked":""}> ${I18N.t("sre.pb_continue_next","继续下一步")}</label></div>
      </div>
      <label class="switch" style="display:flex;margin:2px 0 10px"><input type="checkbox" class="pb-step-ignore" ${s.ignore_exit?"checked":""}> ${I18N.t("sre.pb_ignore_exit","忽略非零退出码（grep 无匹配、diff 有差异等也算成功）")}</label>
      <button class="btn danger sm pb-step-del" type="button">${I18N.t("ui.delete_step")}</button>
    </div>`;
  }).join("");
  c.querySelectorAll(".pb-step-del").forEach(btn => {
    btn.onclick = () => { btn.closest(".pb-step").remove(); };
  });
  // Initialize target tree pickers + module visibility
  c.querySelectorAll(".pb-step").forEach(step => paintPbTargetPicker(step));
  c.querySelectorAll(".pb-step-module").forEach(sel => pbModuleChange(sel));
}

// Show only the argument block matching the step's selected type (module).
function pbModuleChange(sel) {
  const step = sel.closest(".pb-step");
  if (!step) return;
  step.querySelectorAll(".pb-mod").forEach(m => { m.style.display = "none"; });
  const key = sel.value === "" ? "shell" : sel.value;
  const show = step.querySelector(".pb-mod-" + key);
  if (show) show.style.display = "";
}

// Gather module-specific arguments from a step's form into an args object.
function collectModuleArgs(el, mod) {
  const args = {};
  const g = (cls) => { const n = el.querySelector(cls); return n ? n.value.trim() : ""; };
  if (mod === "service") {
    args.name = g(".pb-arg-service-name");
    args.state = g(".pb-arg-service-state");
    const en = g(".pb-arg-service-enabled"); if (en) args.enabled = en;
  } else if (mod === "package") {
    args.name = g(".pb-arg-package-name");
    args.state = g(".pb-arg-package-state");
  } else if (mod === "copy") {
    args.dest = g(".pb-arg-copy-dest");
    const cont = el.querySelector(".pb-arg-copy-content");
    args.content = cont ? cont.value : "";
    const mode = g(".pb-arg-copy-mode"); if (mode) args.mode = mode;
  } else if (mod === "service_status") {
    args.name = g(".pb-arg-svcstatus-name");
  } else if (mod === "file_stat" || mod === "file_head") {
    args.path = g(".pb-arg-filepath");
  } else if (mod === "dns_resolve") {
    args.host = g(".pb-arg-dns-host");
  } else if (mod === "journal_recent") {
    const lines = g(".pb-arg-journal-lines"); if (lines) args.lines = lines;
  } else if (mod === "kube_get") {
    const res = g(".pb-arg-kube-resource"); if (res) args.resource = res;
  } else if (mod === "host_inspect") {
    const profile = g(".pb-arg-inspect-profile");
    args.profile = profile || "standard";
  } else if (mod === "container_logs") {
    const id = g(".pb-arg-ctr-id"); if (id) args.id = id;
    const tail = g(".pb-arg-ctr-tail"); if (tail) args.tail = tail;
  } else if (mod === "container_action") {
    const id = g(".pb-arg-ctr-act-id"); if (id) args.id = id;
    args.action = g(".pb-arg-ctr-act") || "restart";
  } else if (mod === "container_compose") {
    const p = g(".pb-arg-compose-project");
    if (p) { if (p.includes("/") || p.endsWith(".yml") || p.endsWith(".yaml")) args.file = p; else args.project = p; }
    args.action = g(".pb-arg-compose-act") || "ps";
  } else if (mod === "hyperv_power") {
    const n = g(".pb-arg-hv-name"); if (n) args.name = n;
    args.action = g(".pb-arg-hv-power") || "start";
  } else if (mod === "hyperv_set") {
    const n = g(".pb-arg-hvset-name"); if (n) args.name = n;
    const cpu = g(".pb-arg-hvset-cpu"); if (cpu) args.processor_count = cpu;
    const mem = g(".pb-arg-hvset-mem"); if (mem) args.memory_mb = mem;
  }
  return args;
}

// Playbook step target: folder tree (hostname + IP) via shared HostPicker.
const PB_STEP_PICK = new WeakMap();
let _pbPickUid = 0;

/** Expand legacy all / system: tokens into folder:/host: tree selections. */
function pbNormalizeTargetTokens(rawTokens, hosts) {
  const set = new Set();
  const src = rawTokens instanceof Set ? rawTokens : HostPicker.parseTargetTokens(rawTokens);
  const list = [...src];
  if (!list.length) return set;
  if (list.length === 1 && list[0] === "all") {
    const folders = HostPicker.folderTree();
    const walk = (nodes) => {
      (nodes || []).forEach(n => {
        set.add("folder:" + n.id);
        walk(n.children || []);
      });
    };
    walk(folders);
    if ((hosts || []).some(h => !(h.folder_id || "").trim())) set.add("folder:__ungrouped__");
    if (!set.size) (hosts || []).forEach(h => set.add("host:" + h.id));
    return set;
  }
  list.forEach(tok => {
    if (!tok || tok === "all") return;
    if (tok.startsWith("system:")) {
      const sys = tok.slice(7);
      (hosts || []).forEach(h => {
        if (pbHostMatchesSystem(h, sys)) set.add("host:" + h.id);
      });
      return;
    }
    set.add(tok);
  });
  return set;
}

function pbFolderSubtreeIds(fid) {
  const ids = new Set([fid]);
  if (fid === "__ungrouped__") return ids;
  const folders = (typeof HOST_FOLDERS !== "undefined" && HOST_FOLDERS && HOST_FOLDERS.folders) ? HOST_FOLDERS.folders : [];
  const walk = (nodes) => {
    for (const n of nodes || []) {
      if (n.id === fid) {
        const collect = (x) => { ids.add(x.id); (x.children || []).forEach(collect); };
        collect(n);
        return true;
      }
      if (walk(n.children || [])) return true;
    }
    return false;
  };
  walk(folders);
  return ids;
}

function paintPbTargetPicker(step) {
  const wrap = step.querySelector(".pb-step-target-picker");
  const hidden = step.querySelector(".pb-step-target");
  if (!wrap || !hidden) return;
  if (!window.HostPicker) {
    wrap.innerHTML = `<div class="hint">${esc(I18N.t("playbook.target_fallback", "主机选择器未加载"))}</div>`;
    return;
  }
  let st = PB_STEP_PICK.get(step);
  if (!st) {
    st = { collapsed: new Set(), q: "", uid: "pb" + (++_pbPickUid), tokens: null };
    PB_STEP_PICK.set(step, st);
  }
  if (!st.tokens) {
    st.tokens = pbNormalizeTargetTokens(hidden.value || "", PB_HOSTS);
    hidden.value = HostPicker.serializeTargetTokens(st.tokens);
  }
  const syncHidden = () => {
    hidden.value = HostPicker.serializeTargetTokens(st.tokens);
    pbTargetPreviewFromStep(step);
  };
  wrap.innerHTML = HostPicker.renderHTML({
    id: "pbTgt_" + st.uid,
    name: "pb_tgt_" + st.uid,
    mode: "target",
    hosts: PB_HOSTS,
    targetTokens: st.tokens,
    targetValue: HostPicker.serializeTargetTokens(st.tokens),
    collapsed: st.collapsed,
    q: st.q,
    compact: true,
  });
  const root = wrap.querySelector(".host-picker");
  if (root) root._hpBound = false;
  HostPicker.bind(root, {
    onToggleFold: (id) => {
      if (st.collapsed.has(id)) st.collapsed.delete(id); else st.collapsed.add(id);
      paintPbTargetPicker(step);
    },
    onSearch: (q) => { st.q = q; st._focusSearch = true; paintPbTargetPicker(step); },
    onQuick: (act) => {
      if (act === "clear") {
        st.tokens = new Set();
      } else if (act === "all-online") {
        st.tokens = new Set();
        PB_HOSTS.filter(h => h.online).forEach(h => st.tokens.add("host:" + h.id));
      } else if (act === "all-visible") {
        const q = (st.q || "").trim().toLowerCase();
        st.tokens = new Set();
        PB_HOSTS.filter(h => HostPicker.filterHost(h, q)).forEach(h => st.tokens.add("host:" + h.id));
      }
      syncHidden();
      paintPbTargetPicker(step);
    },
    onTargetToggle: (token, checked) => {
      st.tokens.delete("all");
      if (checked) {
        st.tokens.add(token);
        if (token.startsWith("folder:")) {
          const fid = token.slice(7);
          const ids = pbFolderSubtreeIds(fid);
          PB_HOSTS.forEach(h => {
            const hf = h.folder_id || "__ungrouped__";
            if (ids.has(hf)) st.tokens.delete("host:" + h.id);
          });
        }
      } else {
        st.tokens.delete(token);
        if (token.startsWith("folder:")) {
          const fid = token.slice(7);
          const ids = pbFolderSubtreeIds(fid);
          PB_HOSTS.forEach(h => {
            const hf = h.folder_id || "__ungrouped__";
            if (ids.has(hf)) st.tokens.delete("host:" + h.id);
          });
        }
      }
      syncHidden();
      paintPbTargetPicker(step);
    },
  });
  if (st._focusSearch) {
    st._focusSearch = false;
    HostPicker.focusSearch(wrap);
  }
  syncHidden();
}

// Host join/leave → keep open playbook / change-window host trees in sync.
if (!window._pbHostTreesRefreshBound) {
  window._pbHostTreesRefreshBound = true;
  document.addEventListener("aiops:host-trees-refresh", () => {
    try {
      if (typeof PB_HOSTS !== "undefined" && Array.isArray(LAST_HOSTS)) PB_HOSTS = LAST_HOSTS;
      if (typeof SRE_HOSTS !== "undefined" && Array.isArray(LAST_HOSTS)) SRE_HOSTS = LAST_HOSTS;
    } catch (_) {}
    document.querySelectorAll(".pb-step-target-picker").forEach(wrap => {
      const step = wrap.closest(".pb-step");
      if (step && typeof paintPbTargetPicker === "function") paintPbTargetPicker(step);
    });
    ["cwHostPick", "crHostPick"].forEach(id => {
      const box = typeof $ === "function" ? $(id) : document.getElementById(id);
      if (!box || !box.offsetParent) return;
      const hiddenId = id === "cwHostPick" ? "cwHosts" : "crHosts";
      const hidden = typeof $ === "function" ? $(hiddenId) : document.getElementById(hiddenId);
      const selected = (hidden && hidden.value ? hidden.value.split(",") : []).map(s => s.trim()).filter(Boolean);
      if (typeof sreMountHostMultiPick === "function") sreMountHostMultiPick(id, hiddenId, selected);
    });
  });
  document.addEventListener("aiops:hosts-updated", () => {
    try {
      if (typeof PB_HOSTS !== "undefined" && Array.isArray(LAST_HOSTS)) PB_HOSTS = LAST_HOSTS;
      if (typeof SRE_HOSTS !== "undefined" && Array.isArray(LAST_HOSTS)) SRE_HOSTS = LAST_HOSTS;
    } catch (_) {}
  });
}

// Legacy flat <option> builder kept for any leftover callers / AI helpers.
function buildTargetOptions(selectedTarget) {
  const opts = [`<option value="" ${!selectedTarget?"selected":""}>${I18N.t("playbook.target_none","未选择目标")}</option>`];
  if (PB_CATS.length > 0) {
    opts.push(`<optgroup label="${I18N.t("section.by_category")}">`);
    PB_CATS.forEach(cat => {
      const val = `category:${cat}`;
      opts.push(`<option value="${esc(val)}" ${selectedTarget===val?"selected":""}>${esc(cat)}</option>`);
    });
    opts.push("</optgroup>");
  }
  if (PB_HOSTS.length > 0) {
    opts.push(`<optgroup label="${I18N.t("section.target_host")}">`);
    PB_HOSTS.forEach(h => {
      const val = `host:${h.id}`;
      const lab = (window.HostPicker && HostPicker.optionLabel) ? HostPicker.optionLabel(h) : (h.hostname || h.id);
      opts.push(`<option value="${esc(val)}" ${selectedTarget===val?"selected":""}>${esc(lab)}</option>`);
    });
    opts.push("</optgroup>");
  }
  return opts.join("");
}

// Match playbook system: selectors against host.os (GOOS) + host.platform (pretty).
// Supports optional version suffix: rocky:9, openeuler:22, windows:2022, macos:15.
function pbHostMatchesSystem(h, sys) {
  const raw = (sys || "").toLowerCase();
  const colon = raw.indexOf(":");
  const base = colon >= 0 ? raw.slice(0, colon) : raw;
  const wantVer = colon >= 0 ? raw.slice(colon + 1) : "";
  const os = (h.os || "").toLowerCase();
  const platform = (h.platform || "").toLowerCase();
  const blob = platform + " " + os;
  let ok = false;
  switch (base) {
    case "linux":
      ok = os === "linux"; break;
    case "windows":
      ok = os === "windows"; break;
    case "macos":
    case "darwin":
      ok = os === "darwin" || os === "macos"; break;
    case "rocky":
    case "rockylinux":
      ok = blob.includes("rocky"); break;
    case "kylin":
    case "neokylin":
    case "kylinos":
      ok = blob.includes("kylin") || blob.includes("neokylin"); break;
    case "rhel":
    case "redhat":
      ok = /rhel|red hat|rocky|alma|centos|openeuler|euleros|euler os|anolis|alinux|alibaba cloud|amzn|amazon linux|fedora/.test(blob); break;
    case "centos":
      ok = blob.includes("centos"); break;
    case "alma":
    case "almalinux":
      ok = blob.includes("alma"); break;
    case "ubuntu":
      ok = blob.includes("ubuntu"); break;
    case "debian":
      ok = blob.includes("debian") || blob.includes("ubuntu"); break;
    case "uos":
      ok = blob.includes("uos") || blob.includes("deepin"); break;
    case "openeuler":
      ok = blob.includes("openeuler"); break;
    case "euleros":
    case "euler":
      ok = blob.includes("euleros") || blob.includes("euler os"); break;
    case "alinux":
    case "alibaba":
    case "alibabacloudlinux":
      ok = blob.includes("alinux") || blob.includes("alibaba cloud") || blob.includes("alibabacloudlinux"); break;
    case "anolis":
      ok = blob.includes("anolis"); break;
    case "amzn":
    case "amazon":
    case "amazonlinux":
      ok = blob.includes("amazon linux") || blob.includes("amzn"); break;
    case "fedora":
      ok = blob.includes("fedora"); break;
    default:
      ok = os === base || (base === "macos" && os === "darwin") || blob.includes(base);
  }
  if (!ok) return false;
  if (!wantVer) return true;
  // Prefer whole-version tokens (avoid system:rocky:1 matching "9.1" / "10").
  const tokens = blob.split(/[^0-9.]+/).filter(Boolean);
  for (const tok of tokens) {
    if (tok === wantVer || tok.startsWith(wantVer + ".")) return true;
    const maj = tok.match(/^(\d+)/);
    if (maj && maj[1] === wantVer) return true;
  }
  // Windows Server year / explicit substrings like "v11".
  if (/^\d{4}$/.test(wantVer) && blob.includes(wantVer)) return true;
  if (blob.includes("v" + wantVer)) return true;
  return false;
}

function pbCountForTarget(target) {
  const parts = String(target || "").split(",").map(s => s.trim()).filter(Boolean);
  if (!parts.length) return 0;
  if (parts.includes("all")) return PB_HOSTS.length;
  const ids = new Set();
  parts.forEach(p => {
    if (p.startsWith("folder:")) {
      const fid = p.slice("folder:".length);
      const fids = pbFolderSubtreeIds(fid);
      PB_HOSTS.forEach(h => {
        const hf = h.folder_id || "__ungrouped__";
        if (fids.has(hf)) ids.add(h.id);
      });
    } else if (p.startsWith("category:")) {
      const cat = p.slice("category:".length);
      PB_HOSTS.forEach(h => {
        if ((h.category || I18N.t("section.uncategorized")) === cat) ids.add(h.id);
      });
    } else if (p.startsWith("system:")) {
      const sys = p.slice("system:".length).toLowerCase();
      PB_HOSTS.forEach(h => { if (pbHostMatchesSystem(h, sys)) ids.add(h.id); });
    } else if (p.startsWith("host:")) {
      ids.add(p.slice(5));
    }
  });
  return ids.size;
}

function pbTargetPreviewFromStep(step) {
  if (!step) return;
  const preview = step.querySelector(".pb-target-preview");
  const hidden = step.querySelector(".pb-step-target");
  if (!preview || !hidden) return;
  const target = hidden.value || "";
  const count = pbCountForTarget(target);
  const label = (window.HostPicker && HostPicker.labelForTarget)
    ? HostPicker.labelForTarget(target, PB_HOSTS)
    : (target || I18N.t("empty.no_host_match2"));
  preview.textContent = count > 0
    ? `${I18N.t("ui.matched")} ${count} ${I18N.t("ui.hosts_matched")} · ${label}`
    : I18N.t("empty.no_host_match2");
  preview.style.color = count > 0 ? "var(--ok, #31c46b)" : "var(--crit, #ff5b6e)";
}

// Preview matched host count when target changes (legacy select handler)
function pbTargetPreview(sel) {
  const step = sel && sel.closest ? sel.closest(".pb-step") : null;
  if (step) { pbTargetPreviewFromStep(step); return; }
}

function sreHostLabel(h) {
  return (window.HostPicker && HostPicker.optionLabel)
    ? HostPicker.optionLabel(h)
    : ((h.hostname || h.id) + (h.ip ? ` (${h.ip})` : ""));
}

/** Mount multi-select host tree; keep CSV of IDs in hidden input. */
function sreMountHostMultiPick(containerId, hiddenId, selectedIds) {
  const box = $(containerId);
  const hidden = $(hiddenId);
  if (!box) return;
  const hosts = (typeof LAST_HOSTS !== "undefined" && Array.isArray(LAST_HOSTS) && LAST_HOSTS.length)
    ? LAST_HOSTS
    : ((typeof SRE_HOSTS !== "undefined" && SRE_HOSTS) || (typeof PB_HOSTS !== "undefined" && PB_HOSTS) || []);
  const selected = new Set((selectedIds || []).filter(Boolean));
  const st = box._srePick || { collapsed: new Set(), q: "" };
  box._srePick = st;
  const syncHidden = () => { if (hidden) hidden.value = [...selected].join(","); };
  syncHidden();
  if (!window.HostPicker) {
    box.innerHTML = hosts.map(h =>
      `<label class="host-chk"><input type="checkbox" value="${esc(h.id)}" ${selected.has(h.id) ? "checked" : ""}> ${esc(sreHostLabel(h))}</label>`
    ).join("") || `<span class="muted">暂无主机</span>`;
    box.onchange = () => {
      selected.clear();
      box.querySelectorAll("input:checked").forEach(cb => selected.add(cb.value));
      syncHidden();
    };
    return;
  }
  const paint = () => {
    box.innerHTML = HostPicker.renderHTML({
      id: containerId + "_tree",
      mode: "multi",
      hosts,
      selected,
      collapsed: st.collapsed,
      q: st.q,
      onlineOnly: false,
      compact: true,
    });
    const root = box.querySelector(".host-picker") || box;
    root._hpBound = false;
    HostPicker.bind(root, {
      onToggleFold: (id) => {
        selected.clear(); HostPicker.readMulti(root).forEach(x => selected.add(x));
        if (st.collapsed.has(id)) st.collapsed.delete(id); else st.collapsed.add(id);
        paint();
      },
      onSearch: (q) => {
        selected.clear(); HostPicker.readMulti(root).forEach(x => selected.add(x));
        st.q = q || ""; st._focusSearch = true; paint();
      },
      onQuick: (act) => {
        selected.clear(); HostPicker.readMulti(root).forEach(x => selected.add(x));
        if (act === "clear") selected.clear();
        else if (act === "all-online") hosts.filter(h => h.online).forEach(h => selected.add(h.id));
        else hosts.filter(h => HostPicker.filterHost(h, (st.q || "").trim().toLowerCase())).forEach(h => selected.add(h.id));
        paint();
      },
      onFolderToggle: (fid, checked) => {
        selected.clear(); HostPicker.readMulti(root).forEach(x => selected.add(x));
        const q = (st.q || "").trim().toLowerCase();
        const byFolder = HostPicker.hostsByFolder(hosts);
        let ids = [];
        if (String(fid).startsWith("cat:")) {
          const cat = String(fid).slice(4);
          ids = hosts.filter(h => ((h.category || "").trim() || "未分组") === cat && HostPicker.filterHost(h, q)).map(h => h.id);
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
        ids.forEach(id => { if (checked) selected.add(id); else selected.delete(id); });
        paint();
      },
      onHostToggle: (id, checked) => {
        if (checked) selected.add(id); else selected.delete(id);
        syncHidden();
      },
    });
    if (st._focusSearch) {
      st._focusSearch = false;
      HostPicker.focusSearch(box);
    }
    syncHidden();
  };
  paint();
}

function collectPlaybook() {
  const steps = [];
  document.querySelectorAll("#pbSteps .pb-step").forEach(el => {
    const mod = el.querySelector(".pb-step-module").value;
    const step = {
      name: el.querySelector(".pb-step-name").value.trim(),
      target: el.querySelector(".pb-step-target").value,
      timeout_sec: parseInt(el.querySelector(".pb-step-timeout").value) || 30,
      continue_on_error: el.querySelector(".pb-step-cont").checked,
      ignore_exit: el.querySelector(".pb-step-ignore").checked,
      when: el.querySelector(".pb-step-when").value.trim(),
      register: el.querySelector(".pb-step-register").value.trim(),
      max_attempts: parseInt(el.querySelector(".pb-step-attempts").value) || 3,
      retry_delay_sec: parseInt(el.querySelector(".pb-step-retry-delay").value) || 2,
      retry_on_exit: el.querySelector(".pb-step-retry-exit").checked,
      rollback: el.querySelector(".pb-step-rollback").value.trim(),
      rollback_win: el.querySelector(".pb-step-rollback-win").value.trim(),
      rollback_mac: el.querySelector(".pb-step-rollback-mac").value.trim()
    };
    if (mod) {
      step.module = mod;
      step.args = collectModuleArgs(el, mod);
    } else {
      step.command = el.querySelector(".pb-step-cmd").value.trim();
      step.command_win = el.querySelector(".pb-step-cmdwin").value.trim();
      step.command_mac = el.querySelector(".pb-step-cmdmac").value.trim();
    }
    steps.push(step);
  });
  let schedule = null;
  if ($("pbSchedEnabled").checked) {
    const kind = $("pbSchedKind").value;
    schedule = { enabled: true, kind };
    if (kind === "interval") schedule.interval_min = parseInt($("pbSchedInterval").value) || 0;
    if (kind === "daily" || kind === "weekly") schedule.at = $("pbSchedAt").value.trim();
    if (kind === "weekly") schedule.weekday = parseInt($("pbSchedWeekday").value) || 0;
  }
  const strategy = {
    max_parallel: Math.max(1, Math.min(100, parseInt($("pbMaxParallel").value) || 30)),
    auto_rollback: $("pbAutoRollback").checked
  };
  return { id: $("pbId").value, name: $("pbName").value.trim(), description: $("pbDesc").value.trim(), steps, strategy, schedule };
}

async function savePlaybook() {
  const pb = collectPlaybook();
  if (!pb.name) { toast(I18N.t("valid.fill_playbook_name"), "err"); return; }
  if (pb.steps.length === 0) { toast(I18N.t("valid.need_step"), "err"); return; }
  const missing = (pb.steps || []).findIndex(s => !String(s.target || "").trim());
  if (missing >= 0) {
    toast(I18N.t("valid.need_step_target", "请为每个步骤在主机树中勾选目标（分组或主机）") + ` (#${missing + 1})`, "err");
    return;
  }
  await withLoading("pbSaveBtn", async () => {
    try {
      const r = await fetch(`${API}/playbooks`, { method: "POST", headers: {"Content-Type":"application/json"}, body: JSON.stringify(pb) });
      const j = await r.json().catch(()=>({}));
      if (r.ok) { toast(I18N.t("toast.playbook_saved"), "ok"); $("playbookMask").classList.remove("show"); loadPlaybooks(); }
      else toast(j.error || I18N.t("toast.save_failed"), "err");
    } catch (e) { toast(I18N.t("toast.save_failed2") + e, "err"); }
  });
}

async function executePlaybook(id) {
  try {
    let riskAccepted = false;
    const pfResp = await fetch(`${API}/playbooks/${encodeURIComponent(id)}/preflight`);
    const pf = await pfResp.json().catch(()=>({}));
    if (!pfResp.ok || !pf.valid) {
      toast((pf.warnings || []).join("；") || pf.error || "剧本确定性预检未通过", "err");
      return;
    }
    if (pf.risk_level !== "low" || pf.requires_approval || pf.freeze_active || (pf.warnings || []).length) {
      const risk = pf.risk_level === "high" ? "高" : pf.risk_level === "medium" ? "中" : "低";
      const freezeName = pf.freeze_window && pf.freeze_window.name ? pf.freeze_window.name : "";
      const detail = [
        `确定性预检：风险 ${risk}；在线 ${pf.online_targets} 台；离线跳过 ${pf.offline_targets} 台；最大并发 ${pf.max_parallel}。`,
        pf.freeze_active ? (`变更冻结中${freezeName ? "：「"+freezeName+"」" : ""}——禁止未确认直跑，继续即视为人工确认。`) : "",
        pf.auto_rollback ? "失败自动回滚：已启用（仅执行显式回滚命令）。" : "失败自动回滚：未启用。",
        ...(pf.warnings || [])
      ].filter(Boolean).join("\n");
      const ok = typeof uiConfirm === "function"
        ? await uiConfirm({ title: I18N.t("ui.execute", "执行"), message: detail + "\n\n确认继续执行？", tone: "danger" })
        : confirm(detail + "\n\n确认继续执行？");
      if (!ok) return;
      riskAccepted = !!(pf.requires_approval || pf.freeze_active);
    }
    const headers = riskAccepted ? {"X-AIOps-Risk-Accepted": "true"} : {};
    const r = await fetch(`${API}/playbooks/${encodeURIComponent(id)}/execute`, { method: "POST", headers });
    const j = await r.json().catch(()=>({}));
    if (r.ok) {
      toast(I18N.t("toast.playbook_started"), "ok");
      // Poll for result
      const execId = j.execution_id;
      pollExecution(execId, id);
    } else toast(j.error || I18N.t("toast.execute_failed"), "err");
  } catch (e) { toast(I18N.t("toast.execute_failed2") + e, "err"); }
}

async function pollExecution(execId, pbId) {
  $("execResultTitle").textContent = translateExecStatus("running");
  $("execResultBody").innerHTML = `<div class="empty-line">${I18N.t("ui.executing")}</div>`;
  $("execResultMask").classList.add("show");
  // Long playbooks (host_inspect / security) can run several minutes — keep
  // polling until terminal status or ~30 minutes, with mild backoff.
  // compact=1：仅回传输出预览，避免多机巡检时每轮拉数十 MB JSON 卡死页面。
  let delay = 1500;
  const deadline = Date.now() + 30 * 60 * 1000;
  let lastSig = "";
  while (Date.now() < deadline) {
    await new Promise(r => setTimeout(r, delay));
    try {
      const exec = await fetch(`${API}/playbooks/executions/by-id/${encodeURIComponent(execId)}?compact=1`).then(r => r.json());
      const sig = pbExecProgressSig(exec);
      if (sig !== lastSig) {
        lastSig = sig;
        renderExecResult(exec, { compact: true });
      } else {
        $("execResultTitle").textContent = translateExecStatus(exec.status);
      }
      if (exec.status !== "running" && exec.status !== "pending_approval") break;
      if (delay < 4000) delay += 250;
    } catch (e) {}
  }
}

function pbExecProgressSig(exec) {
  if (!exec) return "";
  const parts = [exec.status || "", exec.end_time || 0];
  Object.entries(exec.host_results || {}).forEach(([hid, r]) => {
    parts.push(hid, r.status || "", r.reason || "", (r.steps || []).length);
    (r.steps || []).forEach(s => parts.push(s.status || "", s.duration_ms || 0, (s.output || "").length));
  });
  return parts.join("|");
}

function pbTruncateOut(s, max) {
  s = String(s || "");
  max = max || 3500;
  if (s.length <= max) return esc(s);
  return esc(s.slice(0, max)) + `\n… (${s.length} ${I18N.t("exec.chars", "字符")}，${I18N.t("exec.click_expand", "点击展开全文")})`;
}

function renderExecResult(exec, opts) {
  opts = opts || {};
  window._lastExecResult = exec; // 供「AI 复盘」按钮取用
  $("execResultTitle").textContent = translateExecStatus(exec.status);
  // 有任何主机未成功 → 显示「AI 复盘」按钮（执行中不显示）
  const rb = $("execRetroBtn");
  if (rb) {
    const done = exec.status !== "running" && exec.status !== "pending_approval";
    const hasFail = exec.status === "failed" || exec.status === "partial" || exec.status === "cancelled" || Object.values(exec.host_results || {}).some(r => r.status !== "success");
    rb.style.display = (done && hasFail) ? "" : "none";
  }
  const cb = $("execCancelBtn");
  if (cb) {
    const canStop = exec.status === "running" || exec.status === "pending_approval";
    cb.style.display = canStop ? "" : "none";
    cb.disabled = false;
    cb.onclick = () => cancelPlaybookExecution(exec.id);
  }
  const pending = exec.status === "pending_approval";
  const approveBar = pending ? `<div class="exec-approve-bar" style="display:flex;gap:8px;margin:10px 0;flex-wrap:wrap;align-items:center">
      <span class="badge warn">${esc(exec.risk_note || "定时高风险剧本待审批")}</span>
      <button type="button" class="btn sm primary" id="execApproveBtn">批准执行</button>
      <button type="button" class="btn sm danger" id="execRejectBtn">拒绝</button>
    </div>` : "";
  const running = exec.status === "running";
  const hostEntries = Object.entries(exec.host_results || {});
  const doneN = hostEntries.filter(([, r]) => r.status && r.status !== "pending" && r.status !== "running").length;
  const progress = hostEntries.length
    ? `<div class="hint" style="margin:6px 0">${I18N.t("exec.host_progress", "主机进度")}：${doneN}/${hostEntries.length}${running ? " · " + I18N.t("ui.executing", "执行中…") : ""}</div>`
    : "";
  const rows = hostEntries.map(([hid, r]) => {
    const statusCls = r.status === "success" ? "ok" : (r.status === "failed" || r.status === "timeout") ? "crit" : (r.status === "cancelled" ? "warn" : "warn");
    const reason = r.reason ? ` <span class="mono muted">(${esc(r.reason)})</span>` : "";
    const steps = (r.steps || []).map((s, si) => {
      const out = s.output || "";
      const big = out.length > 3500 || /truncated|完整巡检报告/.test(out);
      const body = (opts.compact || running || big) ? pbTruncateOut(out, 3500) : esc(out);
      const expand = (big || opts.compact) && !running
        ? `<button type="button" class="btn sm ghost exec-expand-out" data-exec-id="${esc(String(exec.id))}" data-host-id="${esc(hid)}" data-step-idx="${si}">${I18N.t("exec.expand_out", "展开全文")}</button>`
        : "";
      return `<div class="exec-step ${esc(s.status || "")}"><span class="exec-step-name">${esc(s.name)}</span><span class="exec-step-status">${translateStepStatus(s.status)}</span>${expand}<pre class="exec-step-out">${body}</pre></div>`;
    }).join("");
    return `<div class="exec-row" data-host-id="${esc(hid)}">
      <div class="exec-row-head"><strong>${esc(r.hostname)}</strong> <span class="badge ${statusCls}">${translateExecStatus(r.status)}</span>${reason}</div>
      <div class="exec-steps">${steps || (r.status === "pending" || r.status === "running" ? `<div class="hint">${I18N.t("exec.waiting_steps", "等待步骤输出…")}</div>` : "")}</div>
    </div>`;
  }).join("");
  const failAgg = (() => {
    const counts = {};
    Object.values(exec.host_results || {}).forEach(r => {
      if (r.status === "success" || r.status === "pending" || r.status === "running") return;
      const k = r.reason || r.status || "unknown";
      counts[k] = (counts[k] || 0) + 1;
    });
    const parts = Object.entries(counts).map(([k, n]) => `${k}:${n}`);
    return parts.length ? `<div class="hint" style="margin:6px 0">失败聚合：${esc(parts.join(" · "))}</div>` : "";
  })();
  $("execResultBody").innerHTML = `<div class="exec-meta">${I18N.t("exec.operator")}: ${esc(exec.operator)} · ${I18N.t("exec.start_time")}: ${fmtDateTime(exec.start_time)}${exec.end_time?" · "+I18N.t("exec.end_time")+": "+fmtDateTime(exec.end_time):""} · ${I18N.t("exec.status_label")}: ${translateExecStatus(exec.status)}${exec.trigger ? " · " + esc(exec.trigger) : ""}</div>${approveBar}${progress}${failAgg}${rows}`;
  const ab = $("execApproveBtn"), rj = $("execRejectBtn");
  if (ab) ab.onclick = async () => {
    const r = await fetch(`${API}/playbooks/executions/by-id/${encodeURIComponent(exec.id)}/approve`, { method: "POST" });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) { toast(j.error || "批准失败", "err"); return; }
    toast("已批准，开始执行", "ok");
    pollExecution(exec.id, exec.playbook_id);
  };
  if (rj) rj.onclick = async () => {
    const ok = typeof uiConfirm === "function"
      ? await uiConfirm({ title: "拒绝执行", message: "确认拒绝该定时剧本执行？", tone: "danger" })
      : confirm("确认拒绝该定时剧本执行？");
    if (!ok) return;
    const r = await fetch(`${API}/playbooks/executions/by-id/${encodeURIComponent(exec.id)}/reject`, { method: "POST" });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) { toast(j.error || "拒绝失败", "err"); return; }
    toast("已拒绝", "ok");
    const exec2 = await fetch(`${API}/playbooks/executions/by-id/${encodeURIComponent(exec.id)}?compact=1`).then(x => x.json());
    renderExecResult(exec2, { compact: true });
  };
  $("execResultBody").querySelectorAll(".exec-expand-out").forEach(btn => {
    btn.onclick = async () => {
      try {
        btn.disabled = true;
        const full = await fetch(`${API}/playbooks/executions/by-id/${encodeURIComponent(btn.dataset.execId)}`).then(r => r.json());
        const hr = (full.host_results || {})[btn.dataset.hostId];
        const step = hr && (hr.steps || [])[parseInt(btn.dataset.stepIdx, 10)];
        const pre = btn.parentElement && btn.parentElement.querySelector(".exec-step-out");
        if (pre && step) pre.textContent = step.output || "";
        btn.remove();
      } catch (e) {
        toast(I18N.t("toast.load_failed", "加载失败") + ": " + e, "err");
        btn.disabled = false;
      }
    };
  });
}

async function cancelPlaybookExecution(execId) {
  if (!execId) return;
  const ok = typeof uiConfirm === "function"
    ? await uiConfirm({
        title: I18N.t("exec.stop", "停止"),
        message: I18N.t("exec.confirm_cancel", "确认彻底停止该剧本执行？未开始的主机将不再下发任务；进行中的会话会中止（不会向主机下发 kill 脚本）。"),
        tone: "danger"
      })
    : confirm(I18N.t("exec.confirm_cancel", "确认彻底停止该剧本执行？未开始的主机将不再下发任务；进行中的会话会中止（不会向主机下发 kill 脚本）。"));
  if (!ok) return;
  const btn = $("execCancelBtn");
  if (btn) btn.disabled = true;
  try {
    const r = await fetch(`${API}/playbooks/executions/by-id/${encodeURIComponent(execId)}/cancel`, { method: "POST" });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) {
      toast(j.error || I18N.t("exec.cancel_fail", "停止失败"), "err");
      if (btn) btn.disabled = false;
      return;
    }
    toast(I18N.t("exec.cancel_ok", "已停止剧本执行"), "ok");
    const exec2 = await fetch(`${API}/playbooks/executions/by-id/${encodeURIComponent(execId)}?compact=1`).then(x => x.json());
    renderExecResult(exec2, { compact: true });
  } catch (e) {
    toast(String(e), "err");
    if (btn) btn.disabled = false;
  }
}

async function loadExecHistory() {
  try {
    const list = await fetch(`${API}/playbooks/executions`).then(r => r.json());
    const rows = (list || []).map(e => {
      const success = Object.values(e.host_results || {}).filter(r => r.status === "success").length;
      const total = Object.keys(e.host_results || {}).length;
      const badge = e.status === "completed" ? "ok" : (e.status === "failed" || e.status === "rejected") ? "crit" : "warn";
      const stopBtn = (e.status === "running" || e.status === "pending_approval")
        ? `<button type="button" class="btn sm danger exec-hist-cancel" data-exec-id="${e.id}">${I18N.t("exec.stop", "停止")}</button>`
        : "";
      return `<div class="exec-hist-row" data-exec-id="${e.id}">
        <strong>${esc(e.playbook_name)}</strong>
        <span class="badge ${badge}">${translateExecStatus(e.status)}</span>
        <span class="mono" style="color:var(--muted)">${success}/${total} ${I18N.t("exec.success_count")}</span>
        <span class="mono" style="color:var(--muted)">${fmtDateTime(e.start_time)}</span>
        <span class="mono" style="color:var(--muted)">${esc(e.operator)}${e.trigger === "schedule" ? " · 定时" : ""}</span>
        ${stopBtn}
      </div>`;
    }).join("");
    $("execHistBody").innerHTML = rows || `<div class="empty-line">${I18N.t("empty.no_executions")}</div>`;
    $("execHistBody").querySelectorAll(".exec-hist-cancel").forEach(btn => {
      btn.onclick = async (ev) => {
        ev.stopPropagation();
        await cancelPlaybookExecution(btn.dataset.execId);
        loadExecHistory();
      };
    });
    $("execHistBody").querySelectorAll(".exec-hist-row").forEach(el => {
      el.onclick = async (ev) => {
        if (ev.target && ev.target.closest && ev.target.closest(".exec-hist-cancel")) return;
        const exec = await fetch(`${API}/playbooks/executions/by-id/${encodeURIComponent(el.dataset.execId)}?compact=1`).then(r => r.json());
        renderExecResult(exec, { compact: true });
        $("execHistMask").classList.remove("show");
        $("execResultMask").classList.add("show");
      };
    });
    $("execHistMask").classList.add("show");
  } catch (e) { toast(I18N.t("toast.load_history_failed") + e, "err"); }
}

// Playbook event listeners
safeAddEventListener("addPlaybookBtn", "click", () => openPlaybookModal(null));
safeAddEventListener("pbAddStep", "click", () => {
  const pb = collectPlaybook();
  const steps = pb.steps || [];
  steps.push({ name: "", module: "gather_facts", target: "", timeout_sec: 30, continue_on_error: false });
  renderPbSteps(steps);
});

// 只读运维模板：一键填充多步骤巡检剧本（不修改系统）
const PB_READONLY_TEMPLATES = {
  deep: {
    name: "深度主机巡检（只读）",
    description: "一键 host_inspect：跨 Windows/Linux/macOS（含 Rocky 9/10、麒麟 V10/V11）生成结构化巡检报告（standard 档；告警发现不阻断剧本）",
    steps: [
      {
        name: "深度主机巡检", module: "host_inspect", target: "all",
        timeout_sec: 150, register: "inspect", ignore_exit: true,
        args: { profile: "standard" },
      },
    ]
  },
  sys: {
    name: "系统巡检（只读）",
    description: "单步 host_inspect（quick）：磁盘/内存/CPU/进程等一次采集，避免多步重复拉起 PowerShell",
    steps: [
      {
        name: "系统巡检", module: "host_inspect", target: "all",
        timeout_sec: 90, register: "inspect", ignore_exit: true, continue_on_error: true,
        args: { profile: "quick" },
      },
    ]
  },
  net: {
    name: "网络巡检（只读）",
    description: "网卡、监听端口、路由与连接摘要（只读）",
    steps: [
      { name: "网卡地址", module: "net_ifaces", target: "all", timeout_sec: 20 },
      { name: "监听端口", module: "net_listen", target: "all", timeout_sec: 25 },
      { name: "路由表", module: "net_routes", target: "all", timeout_sec: 15 },
      { name: "连接摘要", module: "net_sockets", target: "all", timeout_sec: 25 },
      { name: "DNS 解析探测", module: "dns_resolve", target: "all", timeout_sec: 15, args: { host: "www.baidu.com" }, continue_on_error: true },
    ]
  },
  sre: {
    name: "SRE可观测巡检（只读）",
    description: "日志、容器、时间同步等只读巡检",
    steps: [
      { name: "系统信息", module: "gather_facts", target: "all", timeout_sec: 25, register: "facts" },
      { name: "最近日志", module: "journal_recent", target: "all", timeout_sec: 35, args: { lines: "80" }, continue_on_error: true },
      { name: "内核消息", module: "dmesg_recent", target: "all", timeout_sec: 25, continue_on_error: true },
      { name: "容器列表", module: "docker_ps", target: "all", timeout_sec: 25, continue_on_error: true },
      { name: "时间时区", module: "time_sync", target: "all", timeout_sec: 15 },
    ]
  },
  sec: {
    name: "安全巡检（只读）",
    description: "登录会话、对外监听、认证失败与主机安全扫描（只读）",
    steps: [
      { name: "登录会话", module: "users_logged", target: "all", timeout_sec: 20 },
      { name: "对外监听", module: "security_listen", target: "all", timeout_sec: 25 },
      { name: "认证失败", module: "auth_failures", target: "all", timeout_sec: 35, continue_on_error: true, ignore_exit: true },
      { name: "主机安全扫描", module: "host_security_scan", target: "all", timeout_sec: 120, continue_on_error: true, ignore_exit: true },
    ]
  },
  container: {
    name: "容器/Compose 巡检（只读）",
    description: "Docker/Podman 容器列表、资源与 Compose 项目（无运行时时软跳过）",
    steps: [
      { name: "容器列表", module: "docker_ps", target: "all", timeout_sec: 25, continue_on_error: true },
      { name: "容器资源", module: "docker_stats", target: "all", timeout_sec: 35, continue_on_error: true },
      { name: "Compose 项目", module: "container_compose_ls", target: "all", timeout_sec: 35, continue_on_error: true, ignore_exit: true },
    ]
  },
  k8s: {
    name: "Kubernetes 巡检（只读）",
    description: "kubectl get pods -A（需节点已配置 kubeconfig）",
    steps: [
      { name: "系统信息", module: "gather_facts", target: "all", timeout_sec: 30, register: "facts" },
      { name: "Pods 一览", module: "kube_get", target: "all", timeout_sec: 60, args: { resource: "pods" }, continue_on_error: true, ignore_exit: true },
      { name: "Nodes", module: "kube_get", target: "all", timeout_sec: 45, args: { resource: "nodes" }, continue_on_error: true, ignore_exit: true },
      { name: "Deployments", module: "kube_get", target: "all", timeout_sec: 45, args: { resource: "deployments" }, continue_on_error: true, ignore_exit: true },
    ]
  },
  hyperv: {
    name: "Hyper-V 宿主巡检（只读+条件）",
    description: "仅 Windows 宿主：系统信息 + 深度巡检；电源类步骤需手工加模块",
    steps: [
      { name: "系统信息", module: "gather_facts", target: "all", timeout_sec: 25, register: "facts", when: "{{os}} == windows" },
      {
        name: "深度巡检", module: "host_inspect", target: "all",
        timeout_sec: 150, register: "inspect", ignore_exit: true, continue_on_error: true,
        when: "{{os}} == windows", args: { profile: "standard" },
      },
      { name: "对外监听", module: "security_listen", target: "all", timeout_sec: 25, when: "{{os}} == windows" },
    ]
  },
  bigdata: {
    name: "大数据巡检（只读）",
    description: "Java 进程与常见大数据端口监听检查（只读）；磁盘/内存走 CIM 批处理缓存",
    steps: [
      { name: "系统信息", module: "gather_facts", target: "all", timeout_sec: 25, register: "facts" },
      { name: "Java进程", module: "bigdata_jps", target: "all", timeout_sec: 25, continue_on_error: true, ignore_exit: true },
      { name: "大数据端口", module: "bigdata_ports", target: "all", timeout_sec: 25, continue_on_error: true },
      { name: "磁盘用量", module: "disk_usage", target: "all", timeout_sec: 20 },
      { name: "内存概况", module: "mem_info", target: "all", timeout_sec: 15 },
    ]
  }
};

async function applyPbTemplate(key) {
  const tpl = PB_READONLY_TEMPLATES[key];
  if (!tpl) return;
  const ok = typeof uiConfirm === "function"
    ? await uiConfirm({
        title: I18N.t("ui.apply", "应用"),
        message: `将用「${tpl.name}」替换当前步骤列表，是否继续？`,
        tone: "danger"
      })
    : confirm(`将用「${tpl.name}」替换当前步骤列表，是否继续？`);
  if (!ok) return;
  $("pbName").value = tpl.name;
  $("pbDesc").value = tpl.description;
  renderPbSteps(tpl.steps.map(s => Object.assign({
    target: "all", timeout_sec: 30, continue_on_error: false, ignore_exit: false, args: {},
  }, s)));
  toast("已套用只读模板：" + tpl.name, "ok");
}

safeAddEventListener("pbTemplateBar", "click", e => {
  const b = e.target.closest("[data-pb-tpl]");
  if (b) applyPbTemplate(b.dataset.pbTpl);
});
safeAddEventListener("pbImportPacksBtn", "click", async () => {
  const btn = $("pbImportPacksBtn");
  await withLoading(btn || "pbImportPacksBtn", async () => {
    try {
      const r = await fetch(`${API}/playbooks/packs/import`, {
        method: "POST", headers: { "Content-Type": "application/json" }, body: "{}"
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) { toast(j.error || "导入失败", "err"); return; }
      toast(`内置剧本包导入完成：新增 ${j.imported || 0}，跳过 ${j.skipped || 0}`, "ok");
      loadPlaybooks();
    } catch (e) { toast("导入失败：" + e, "err"); }
  });
});

// 把编辑器中的剧本对象整理为可读文本，供 AI 预检
function playbookToText(pb) {
  let s = `剧本名称：${pb.name || "(未命名)"}\n描述：${pb.description || "(无)"}\n步骤数：${(pb.steps || []).length}\n`;
  (pb.steps || []).forEach((st, i) => {
    s += `\n步骤${i + 1} [${st.name || "未命名"}] 目标=${st.target} 超时=${st.timeout_sec}s 失败继续=${st.continue_on_error ? "是" : "否"} 忽略退出码=${st.ignore_exit ? "是" : "否"}`;
    if (st.when) s += ` 前置条件=${st.when}`;
    if (st.register) s += ` 存变量=${st.register}`;
    if (st.module) s += `\n  模块：${st.module} 参数：${JSON.stringify(st.args || {})}`;
    else {
      if (st.command) s += `\n  命令(Linux/通用)：${st.command}`;
      if (st.command_win) s += `\n  命令(Windows)：${st.command_win}`;
      if (st.command_mac) s += `\n  命令(macOS)：${st.command_mac}`;
    }
  });
  return s;
}
// 把执行结果整理为聚焦失败的复盘文本
function execResultToText(exec) {
  let s = `剧本：${exec.playbook_name || ""}\n整体状态：${exec.status}\n操作者：${exec.operator || ""}\n`;
  Object.values(exec.host_results || {}).forEach(r => {
    s += `\n主机 ${r.hostname}（${r.status}）：`;
    (r.steps || []).forEach(st => {
      const out = (st.output || "").slice(0, 600);
      s += `\n  - 步骤[${st.name}] ${st.status}` + (st.status !== "success" && out ? `\n    输出：${out}` : "");
    });
  });
  return s.slice(0, 8000);
}
// AI 剧本预检：执行前审查命令的破坏性/幂等性/跨平台/防护缺失，给红黄绿评级
safeAddEventListener("pbPrecheckBtn", "click", () => {
  const pb = collectPlaybook();
  if (!pb.steps || !pb.steps.length) { toast(I18N.t("sre.precheck_need_step","请先添加至少一个步骤再预检"), "err"); return; }
  openAIAssist({
    task: "playbook_precheck",
    title: I18N.t("sre.precheck_title","AI 剧本预检 · 执行前风险审查"),
    mode: "analyze",
    context: playbookToText(pb)
  });
});
// AI 执行复盘：对失败的执行定位根因 + 修复/重跑建议 + 剧本改进
safeAddEventListener("execRetroBtn", "click", () => {
  const exec = window._lastExecResult;
  if (!exec) { toast(I18N.t("sre.retro_no_result","暂无执行结果可复盘"), "err"); return; }
  openAIAssist({
    task: "execution_retro",
    title: I18N.t("sre.retro_title","AI 执行复盘 · 失败根因分析"),
    mode: "analyze",
    context: execResultToText(exec)
  });
});

// AI 辅助：根据自然语言生成整份剧本（名称+描述+步骤），一键回填编辑器
safeAddEventListener("pbAIGenBtn", "click", () => {
  openAIAssist({
    task: "playbook",
    title: I18N.t("sre.pbgen_title","AI 生成运维剧本"),
    mode: "generate",
    placeholder: I18N.t("sre.pbgen_ph","如：滚动重启所有 nginx 主机上的 nginx 服务，任一失败则停止"),
    prefill: ($("pbDesc") && $("pbDesc").value.trim()) || ($("pbName") && $("pbName").value.trim()) || "",
    applyLabel: I18N.t("sre.pbgen_apply","回填到编辑器"),
    applyTo: (text) => {
      try {
        const jsonText = extractFirstCodeBlock(text) || text;
        const pb = JSON.parse(jsonText);
        pb.id = ""; // 作为新剧本回填，保存时另建
        openPlaybookModal(pb);
        if (typeof toast === "function") toast(I18N.t("sre.pbgen_done","已生成，请检查步骤与命令后保存"), "ok");
      } catch (e) {
        if (typeof toast === "function") toast(I18N.t("sre.pbgen_bad_json","AI 输出不是合法剧本 JSON，请查看后手动填写"), "err");
      }
    }
  });
});
safeAddEventListener("pbSaveBtn", "click", savePlaybook);
safeAddEventListener("pbSchedEnabled", "change", pbSchedRefresh);
safeAddEventListener("pbSchedKind", "change", pbSchedRefresh);
safeAddEventListener("pbHistoryBtn", "click", loadExecHistory);
safeAddEventListener("playbookList", "click", async e => {
  const card = e.target.closest(".pb-card"); if (!card) return;
  const act = e.target.closest("[data-pbact]"); if (!act) return;
  const id = card.dataset.id;
  if (act.dataset.pbact === "exec") executePlaybook(id);
  else if (act.dataset.pbact === "edit") {
    fetch(`${API}/playbooks`).then(r=>r.json()).then(pbs => {
      const pb = pbs.find(p=>p.id===id); if (pb) openPlaybookModal(pb);
    });
  } else if (act.dataset.pbact === "del") {
    const ok = typeof uiConfirm === "function"
      ? await uiConfirm({
          title: I18N.t("ui.delete", "删除"),
          message: I18N.t("valid.confirm_delete_playbook"),
          tone: "danger"
        })
      : confirm(I18N.t("valid.confirm_delete_playbook"));
    if (!ok) return;
    fetch(`${API}/playbooks/${encodeURIComponent(id)}`, {method:"DELETE"}).then(()=>{toast(I18N.t("toast.deleted"),"ok");loadPlaybooks();});
  }
});

// ============ SRE 中枢：事件 / 自动修复 / SLO / 工单 ============
let SRE_TAB = "incidents";
let SRE_HOSTS = [], SRE_PLAYBOOKS = [], SRE_CHECKS = [], SRE_RULES = [], SRE_SLOS = [], SRE_TICKETS = [], SRE_API_ENDPOINTS = [];
const SRE_ALERT_TYPES = ["cpu","memory","disk","diskio","iops","gpu","load","proc","conn","hardware","offline","check","host_security","web_security","web_vuln","slow_sql"];
const _sevCls = s => s==="critical"?"crit":s==="warning"?"warn":"info";
const _srcLabel = s => ({alert:I18N.t("sre.src_alert","告警"),slo:"SLO",manual:I18N.t("sre.src_manual","手动")})[s]||esc(s);
const _incStatus = s => ({open:I18N.t("sre.inc_open","进行中"),acknowledged:I18N.t("sre.inc_acked","已确认"),resolved:I18N.t("sre.inc_resolved","已解决")})[s]||esc(s);
const _incStatusCls = s => s==="resolved"?"ok":s==="acknowledged"?"warn":"crit";
const _tlKind = k => ({created:I18N.t("sre.tl_created","创建"),fired:I18N.t("sre.tl_fired","触发"),recovered:I18N.t("sre.tl_recovered","恢复"),acked:I18N.t("sre.tl_acked","确认"),resolved:I18N.t("sre.tl_resolved","解决"),remediation:I18N.t("sre.tl_remediation","自动修复"),comment:I18N.t("sre.tl_comment","评论"),escalated:I18N.t("sre.tl_escalated","升级工单"),note:I18N.t("sre.tl_note","备注"),ai_diagnosis:I18N.t("sre.tl_ai_diagnosis","🤖 AI 诊断"),correlation:I18N.t("sre.tl_correlation","🔗 关联分析"),change_correlation:I18N.t("sre.tl_change_corr","📦 关联变更"),topology_rca:I18N.t("sre.tl_topology_rca","🧭 拓扑 RCA"),ai_analysis:I18N.t("sre.tl_ai_analysis","🤖 AI 分析")})[k]||k;
const _runStatus = s => ({running:I18N.t("sre.run_running","执行中"),success:I18N.t("sre.run_success","成功"),failed:I18N.t("sre.run_failed","失败"),pending_approval:I18N.t("sre.run_pending","待审批"),skipped_cooldown:I18N.t("sre.run_skip_cooldown","冷却跳过"),skipped_ratelimit:I18N.t("sre.run_skip_ratelimit","限频跳过"),rejected:I18N.t("sre.run_rejected","已拒绝"),no_playbook:I18N.t("sre.run_no_playbook","无剧本"),dry_run:I18N.t("sre.run_dry_run","演练"),rolling_back:I18N.t("sre.run_rolling_back","回滚中")})[s]||s;
const _runCls = s => s==="success"?"ok":(s==="failed"||s==="no_playbook")?"crit":s==="pending_approval"?"warn":s.indexOf("skipped")===0||s==="rejected"?"warn":"info";
const _prioCls = p => p==="p1"?"crit":p==="p2"?"warn":"info";
const _tkStatusCls = s => (s==="resolved"||s==="closed")?"ok":s==="in_progress"?"warn":"info";

async function loadSRE(){
  try {
    const [hosts, pbs] = await Promise.all([
      fetch(`${API}/hosts`).then(r=>r.json()),
      fetch(`${API}/playbooks`).then(r=>r.json())
    ]);
    SRE_HOSTS = hosts||[]; SRE_PLAYBOOKS = pbs||[];
  } catch(e){}
  try { SRE_CHECKS = (await fetch(`${API}/checks`).then(r=>r.json()))||[]; } catch(e){ SRE_CHECKS=[]; }
  loadSRETab(SRE_TAB); loadSREBadge();
}
async function loadSREBadge(){
  try {
    const o = await fetch(`${API}/sre/overview`).then(r=>r.json());
    const b = $("navSre"), n = (o.open_incidents||0)+(o.pending_remediations||0);
    if (b){ b.textContent=n; b.style.display=n>0?"":"none"; }
  } catch(e){}
}
function switchSRETab(tab){
  SRE_TAB = tab;
  document.querySelectorAll("#sreTabs .chip-btn").forEach(b=>b.classList.toggle("active", b.dataset.sretab===tab));
  document.querySelectorAll(".sre-panel").forEach(p=>p.classList.toggle("active", p.id==="srePanel-"+tab));
  loadSRETab(tab);
}
function loadSRETab(tab){
  if (tab==="incidents") loadIncidents();
  else if (tab==="remediation") loadRemediation();
  else if (tab==="topology"){ loadTopology(); loadBusinessServices(); }
  else if (tab==="slo") loadSLOs();
  else if (tab==="tickets") loadTickets();
  else if (tab==="oncall") loadOnCall();
  else if (tab==="changes") loadChanges();
  else if (tab==="ai"){ loadInspections(); loadSREEffect(); loadAIRuns(); }
}

/* ---- 事件 ---- */
async function loadIncidents(){
  const el = $("incidentList");
  if (el) el.innerHTML = `<div class="empty-line">${I18N.t("common.loading","加载中…")}</div>`;
  try {
    const list = await fetch(`${API}/incidents`).then(r=>r.json());
    if (!el) return;
    if (!list||!list.length){ el.innerHTML=`<div class="empty-line">${I18N.t("sre.no_incidents","暂无事件")}</div>`; return; }
    el.innerHTML = list.map(i=>`<div class="sre-row" data-incident="${i.id}">
      <span class="badge ${_sevCls(i.severity)}">${esc(i.severity)}</span>
      <div class="sre-row-main"><div class="sre-row-title">${esc(i.title)}</div>
        <div class="sre-row-sub">#${i.id} · ${_srcLabel(i.source)}${i.hostname?" · "+esc(i.hostname):""} · ${fmtDateTime(i.created_at)}</div></div>
      <span class="badge ${_incStatusCls(i.status)}">${_incStatus(i.status)}</span></div>`).join("");
    el.querySelectorAll("[data-incident]").forEach(r=>r.onclick=()=>openIncidentDetail(r.dataset.incident));
  } catch(e){ if (el) el.innerHTML=`<div class="empty-line">${I18N.t("sre.load_failed","加载失败")}</div>`; toast(I18N.t("sre.load_failed","加载失败")+": "+e,"err"); }
}
async function openIncidentDetail(id){
  try {
    const inc = await fetch(`${API}/incidents/${id}`).then(r=>r.json());
    $("incidentDetailTitle").textContent = `#${inc.id} ${inc.title}`;
    const tl = (inc.timeline||[]).slice().reverse().map(e=>{
      const cites=(e.kind==="ai_diagnosis"&&e.citations&&e.citations.length&&typeof renderAssistCitations==="function")
        ? renderAssistCitations(e.citations,{open:false}) : "";
      return `<div class="tl-item">
      <div class="tl-dot ${_sevCls(inc.severity)}"></div>
      <div class="tl-body"><div class="tl-head"><b>${_tlKind(e.kind)}</b> <span class="tl-time">${fmtDateTime(e.ts)}</span>${e.actor?` · <span class="tl-actor">${esc(e.actor)}</span>`:""}</div>${e.text?`<div class="tl-text">${esc(e.text)}</div>`:""}${cites}${typeof attachChipsHTML==="function"?attachChipsHTML(e.attachments):""}</div></div>`;
    }).join("");
    $("incidentDetailBody").innerHTML = `<div class="sre-meta">
      <span class="badge ${_sevCls(inc.severity)}">${esc(inc.severity)}</span>
      <span class="badge ${_incStatusCls(inc.status)}">${_incStatus(inc.status)}</span>
      <span class="mono" style="color:var(--muted)">${_srcLabel(inc.source)}${inc.hostname?" · "+esc(inc.hostname):""}</span>
      ${inc.ticket_id?`<span class="mono" style="color:var(--muted)">🎫 ${I18N.t("sre.ticket","工单")} #${inc.ticket_id}</span>`:""}
      ${(inc.links&&inc.links.length)?`<span class="mono" style="color:var(--muted)">🔗 ${(inc.links||[]).slice(0,6).map(l=>esc(l.type)+":"+esc(l.id)).join(" · ")}</span>`:""}</div>
      <div id="incLoopStrip" class="inc-loop-strip"><div class="empty-line">${I18N.t("sre.loop_loading","加载闭环状态…")}</div></div>
      <div class="subhead">${I18N.t("sre.timeline","时间线")}</div><div class="timeline">${tl||`<div class="empty-line">—</div>`}</div>
      <div class="subhead" style="margin-top:12px">📦 ${I18N.t("sre.related_changes","关联变更")}</div>
      <div id="incRelatedChanges" class="sre-list"><div class="empty-line">加载中…</div></div>
      <div class="subhead" style="margin-top:16px">🤖 ${I18N.t("sre.ai_diag_chat","AI 诊断对话")}</div>
      <div id="incDiagnosisChat" class="ai-diagnosis-chat"></div>
      <div id="incDiagAttach" style="display:none;flex-wrap:wrap;gap:4px;padding:4px 0"></div>
      <div class="ai-diagnosis-input">
        <textarea id="incDiagInput" rows="2" placeholder="${I18N.t("sre.diag_input_ph","追问 AI 细节、反驳结论、要求进一步排查…")}"></textarea>
        <button class="btn sm" id="incDiagAttachBtn" title="${I18N.t("sre.upload_img_file","上传图片或文件")}" style="padding:4px 8px">📎</button>
        <button class="btn primary" id="incDiagSendBtn">${I18N.t("sre.send","发送")}</button>
        <input type="file" id="incDiagFile" multiple hidden>
      </div>
      <label class="ai-term-toggle" id="incTermToggle" style="margin-top:4px;font-size:12px;color:var(--muted);cursor:pointer;display:flex;align-items:center;gap:4px;user-select:none"><input type="checkbox" id="incTermCheck"> ${I18N.t("sre.include_term_ctx","包含终端操作上下文（分段摘要）")}</label>`;
    window._curIncident = inc; // 供「转自动化规则」等操作取用完整事件（含时间线诊断）
    const acts=[];
    // AI 能力收入单一下拉，避免底栏「🤖」按钮连排
    let aiItems=`<button type="button" role="menuitem" data-iact="diagnose">${I18N.t("sre.ai_diagnose","AI 诊断")}</button>
      <button type="button" role="menuitem" data-iact="analysis-board" title="${I18N.t("sre.gen_analysis_board_title","AI 按此事件生成排障分析看板")}">${I18N.t("sre.gen_analysis_board","AI 分析看板")}</button>`;
    if ((inc.timeline||[]).some(e=>e.kind==="ai_diagnosis" && e.text)) {
      aiItems+=`<div class="act-menu-sep"></div>
        <button type="button" role="menuitem" data-iact="propose-fix" title="${I18N.t("sre.propose_fix_title","根据诊断生成一次性修复剧本草稿，审批后在本事件主机执行")}">${I18N.t("sre.propose_fix","生成修复提案")}</button>
        <button type="button" role="menuitem" data-iact="draft-rule" title="${I18N.t("sre.to_auto_rule_title","把诊断建议转成自动修复规则草稿，人工审核后启用")}">${I18N.t("sre.to_auto_rule","转自动化规则")}</button>`;
    }
    acts.push(`<div class="act-menu act-menu-ai drop-up"><button type="button" class="btn sm ai-assist-btn act-menu-trigger" aria-haspopup="true" aria-expanded="false"><span class="ai-assist-btn-ic">🤖</span>AI<span class="act-menu-caret">▾</span></button><div class="act-menu-panel" hidden role="menu">${aiItems}</div></div>`);
    if (inc.host_id) {
      acts.push(`<button class="btn sm" data-iact="topo-rca" title="查看依赖拓扑与变更关联 RCA">🧭 RCA</button>`);
    }
    if (inc.status!=="resolved"){ acts.push(`<button class="btn sm" data-iact="ack">${I18N.t("sre.inc_ack_btn","确认")}</button>`); acts.push(`<button class="btn sm" data-iact="resolve">${I18N.t("sre.inc_resolve_btn","解决")}</button>`); }
    if (!inc.ticket_id) acts.push(`<button class="btn sm" data-iact="escalate">${I18N.t("sre.inc_escalate_btn","升级工单")}</button>`);
    else acts.push(`<button class="btn sm" data-iact="open-ticket">🎫 ${I18N.t("sre.open_ticket","打开工单")} #${inc.ticket_id}</button>`);
    acts.push(`<button class="btn sm" data-iact="emergency-change">应急变更</button>`);
    acts.push(`<button class="btn sm" data-iact="link-sr">关联服务请求</button>`);
    acts.push(`<div class="inc-comment-bar"><div id="incCommentAttach" class="attach-chips" style="display:none"></div><button type="button" class="btn sm" data-iact="comment-attach" title="${I18N.t("sre.upload_img_file","上传图片或文件")}">📎</button><input type="file" id="incCommentFile" multiple hidden accept="${typeof ATTACH_FILE_ACCEPT!=="undefined"?ATTACH_FILE_ACCEPT:"image/*,.txt,.log,.pdf,.docx,.xlsx"}"><input type="text" id="incCommentInput" placeholder="${I18N.t("sre.add_comment_ph","添加评论…")}"><button class="btn primary sm" data-iact="comment">${I18N.t("sre.send","发送")}</button></div>`);
    const foot=$("incidentDetailFoot"); foot.innerHTML=acts.join("");
    window._INC_COMMENT_ATTACHMENTS = [];
    const refreshIncCommentAtt = ()=>renderAttachBox($("incCommentAttach"), window._INC_COMMENT_ATTACHMENTS, i=>{
      window._INC_COMMENT_ATTACHMENTS.splice(i,1); refreshIncCommentAtt();
    });
    foot.querySelectorAll("[data-iact]").forEach(b=>b.onclick=()=>incidentAction(inc.id,b.dataset.iact));
    const incCf=$("incCommentFile");
    if (incCf) incCf.onchange = async ()=>{
      await ingestFilesIntoAttachments(incCf.files, window._INC_COMMENT_ATTACHMENTS, {onChange: refreshIncCommentAtt});
      refreshIncCommentAtt();
      incCf.value="";
    };
    // Wire up diagnosis chat
    window._incDiagId = inc.id;
    window._incDiagHistory = [];
    window._INC_DIAG_ATTACHMENTS = [];
    loadDiagnosisChatHistory(inc.id);
    $("incDiagSendBtn").onclick = () => sendDiagnosisChatMsg();
    $("incDiagInput").onkeydown = e => { if (e.key==="Enter" && !e.shiftKey){ e.preventDefault(); sendDiagnosisChatMsg(); } };
    $("incDiagAttachBtn").onclick = () => { const f=$("incDiagFile"); if(f) f.click(); };
    $("incDiagFile").onchange = onDiagChatFiles;
    renderDiagAttachments();
    $("incidentDetailMask").classList.add("show");
    loadIncidentRelatedChanges(inc.id);
    loadIncidentLoopStrip(inc);
  } catch(e){ toast(I18N.t("sre.load_failed","加载失败")+": "+e,"err"); }
}
async function loadIncidentLoopStrip(inc){
  const el=$("incLoopStrip"); if(!el||!inc) return;
  try{
    const [pages, runs, tk, loopJ]=await Promise.all([
      fetch(`${API}/oncall/pages?open=1`).then(r=>r.json()).catch(()=>[]),
      fetch(`${API}/remediation/runs`).then(r=>r.json()).catch(()=>[]),
      inc.ticket_id ? fetch(`${API}/tickets/${inc.ticket_id}`).then(r=>r.ok?r.json():null).catch(()=>null) : Promise.resolve(null),
      fetch(`${API}/incidents/${inc.id}/loop`).then(r=>r.ok?r.json():null).catch(()=>null)
    ]);
    const page=(pages||[]).find(p=>Number(p.incident_id)===Number(inc.id));
    const pending=(runs||[]).filter(r=>Number(r.incident_id)===Number(inc.id)&&r.status==="pending_approval");
    const rows=[];
    const loop=(loopJ&&loopJ.loop)||inc.loop||{};
    const gate=(loopJ&&loopJ.gate)||{};
    const stages=["diagnosed","dry_run_ok","proposed","approved","verified","promoted"];
    const stageLabel={diagnosed:"诊断",dry_run_ok:"Dry-run",proposed:"提案",approved:"批准",verified:"回验",promoted:"Skill"};
    const cur=loop.stage||"idle";
    const stepHtml=stages.map(st=>{
      const done=stages.indexOf(st)<=stages.indexOf(cur)&&cur!=="idle";
      const active=st===cur;
      return `<span class="badge ${active?"info":(done?"ok":"")}">${esc(stageLabel[st]||st)}</span>`;
    }).join(" → ");
    rows.push(`<div class="inc-loop-row"><b>事件闭环</b>
      <span>${stepHtml||'<span class="hint">idle</span>'}</span>
      ${gate.ok===false?`<span class="badge warn" title="${esc(gate.reason||"")}">闸门</span>`:""}
      <div class="inc-loop-acts">
        <button class="btn sm primary" data-iloop="demo" title="管理员：补诊断证据并自动跑 dry-run→提案→批准→回验→Skill">一键 Demo</button>
        <button class="btn sm" data-iloop="dry-run">Dry-run</button>
        <button class="btn sm" data-iloop="propose">提案</button>
        <button class="btn sm" data-iloop="approve">批准</button>
        <button class="btn sm" data-iloop="verify">回验</button>
        <button class="btn sm" data-iloop="promote">沉淀 Skill</button>
      </div></div>
      <div class="inc-loop-row"><b>案例导出</b>
        <span class="hint">时间线 · 回验 · 变更/会话关联</span>
        <div class="inc-loop-acts"><button class="btn sm" data-iloop="case-export">下载案例包</button></div></div>`);
    if(page){
      const next=page.next_escalate_at?fmtDateTime(page.next_escalate_at):"—";
      const notified=(page.notified||[]).slice(0,6).join(", ")||"—";
      rows.push(`<div class="inc-loop-row"><b>📞 ${I18N.t("sre.oncall_page","值班升级")}</b>
        <span>${I18N.t("sre.oncall_step","阶梯")} ${Number(page.step)||0}</span>
        <span class="badge info">${esc(page.status||"pending")}</span>
        <span>${I18N.t("sre.oncall_notified","已通知")}：${esc(notified)}</span>
        <span>${I18N.t("sre.oncall_next","下次升级")}：${esc(next)}</span></div>`);
    } else {
      rows.push(`<div class="inc-loop-row"><b>📞 ${I18N.t("sre.oncall_page","值班升级")}</b><span class="hint">${I18N.t("sre.oncall_none","暂无进行中的升级页")}</span></div>`);
    }
    if(tk){
      rows.push(`<div class="inc-loop-row"><b>🎫 ${I18N.t("sre.ticket","工单")}</b>
        <span>#${tk.id}</span><span class="badge ${_tkStatusCls(tk.status)}">${esc(tk.status)}</span>
        ${tk.assignee?`<span>@${esc(tk.assignee)}</span>`:""}
        <div class="inc-loop-acts"><button class="btn sm" data-loop="open-ticket">${I18N.t("sre.open_ticket","打开工单")}</button>
        ${(tk.status!=="resolved"&&tk.status!=="closed")?`<button class="btn sm primary" data-loop="close-ticket">${I18N.t("sre.close_ticket","关闭工单")}</button>`:""}</div></div>`);
    } else {
      rows.push(`<div class="inc-loop-row"><b>🎫 ${I18N.t("sre.ticket","工单")}</b><span class="hint">${I18N.t("sre.ticket_none","尚未升级工单")}</span>
        <div class="inc-loop-acts"><button class="btn sm" data-loop="escalate">${I18N.t("sre.inc_escalate_btn","升级工单")}</button></div></div>`);
    }
    if(pending.length){
      rows.push(`<div class="inc-loop-row"><b>🛠 ${I18N.t("sre.pending_fix","待审修复")}</b>
        ${pending.map(r=>{
          const freeze=/变更冻结/.test(String(r.reason||""));
          return `<span>${esc(r.rule_name||r.playbook_name||r.id)}${freeze?` <span class="badge freeze">${I18N.t("sre.freeze_badge","冻结中")}</span>`:""}
            <button class="btn primary sm" data-loop-approve="${r.id}">${I18N.t("sre.approve","批准")}</button>
            <button class="btn danger sm" data-loop-reject="${r.id}">${I18N.t("sre.reject","拒绝")}</button></span>`;
        }).join(" · ")}</div>`);
    } else {
      rows.push(`<div class="inc-loop-row"><b>🛠 ${I18N.t("sre.pending_fix","待审修复")}</b><span class="hint">${I18N.t("sre.pending_fix_none","无待审批项（可先生成修复提案）")}</span></div>`);
    }
    el.innerHTML=rows.join("");
    el.querySelectorAll("[data-loop]").forEach(b=>b.onclick=()=>incidentAction(inc.id,b.dataset.loop));
    el.querySelectorAll("[data-iloop]").forEach(b=>b.onclick=()=>incidentLoopAct(inc.id,b.dataset.iloop,gate));
    el.querySelectorAll("[data-loop-approve]").forEach(b=>b.onclick=async()=>{
      await fetch(`${API}/remediation/runs/${b.dataset.loopApprove}/approve`,{method:"POST"});
      toast(I18N.t("sre.approved_ok","已批准执行"),"ok");
      loadIncidentLoopStrip(inc); loadRemediation(); loadSREBadge();
    });
    el.querySelectorAll("[data-loop-reject]").forEach(b=>b.onclick=async()=>{
      await fetch(`${API}/remediation/runs/${b.dataset.loopReject}/reject`,{method:"POST"});
      toast(I18N.t("sre.rejected_ok","已拒绝"),"ok");
      loadIncidentLoopStrip(inc); loadRemediation(); loadSREBadge();
    });
  }catch(e){
    el.innerHTML=`<div class="empty-line">${I18N.t("sre.loop_load_failed","闭环状态加载失败")}</div>`;
  }
}
async function incidentLoopAct(id, action, gate){
  try{
    if(action==="case-export"){
      const r=await fetch(`${API}/incidents/${id}/case-export`);
      if(!r.ok){ const j=await r.json().catch(()=>({})); toast(j.error||"导出失败","err"); return; }
      const blob=await r.blob();
      const a=document.createElement("a");
      a.href=URL.createObjectURL(blob);
      a.download=`incident-${id}-case.json`;
      a.click();
      URL.revokeObjectURL(a.href);
      toast("案例已导出","ok");
      return;
    }
    if(action==="demo"){
      const ok = typeof uiConfirm === "function"
        ? await uiConfirm({
            title: "一键闭环 Demo",
            message: "将自动补诊断证据（如缺失）并依次执行 dry-run → 提案 → 批准 → 回验 → 沉淀 Skill。仅管理员可用。",
            detail: "适合销售/验收演示；生产事件请改用逐步按钮。",
            confirmText: "开始 Demo",
            tone: "warn"
          })
        : confirm("确认运行一键闭环 Demo？");
      if(!ok) return;
      const r=await fetch(`${API}/incidents/${id}/loop/demo`,{method:"POST",headers:{"Content-Type":"application/json"},body:"{}"});
      const j=await r.json().catch(()=>({}));
      if(!r.ok){ toast(j.error||"Demo 失败","err"); return; }
      toast("一键闭环 Demo 完成","ok");
      openIncidentDetail(id); loadRemediation(); loadSREBadge();
      return;
    }
    let body={};
    if(action==="propose" && gate && gate.ok===false){
      const ok = typeof uiConfirm === "function"
        ? await uiConfirm({ title: I18N.t("ui.confirm","确认"), message: (gate.reason||"诊断闸门未通过")+"\n仍要强制提案？（需管理员）", tone: "danger" })
        : confirm((gate.reason||"诊断闸门未通过")+"\n仍要强制提案？（需管理员）");
      if(!ok) return;
      body.force=true;
    }
    if(action==="promote"){
      const ok = typeof uiConfirm === "function"
        ? await uiConfirm({ title: I18N.t("ui.confirm","确认"), message: "将回验通过的诊断沉淀为 Skill？", tone: "danger" })
        : confirm("将回验通过的诊断沉淀为 Skill？");
      if(!ok) return;
    }
    const r=await fetch(`${API}/incidents/${id}/loop/${action}`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    const j=await r.json().catch(()=>({}));
    if(!r.ok){ toast(j.error||"闭环动作失败","err"); return; }
    if(j.checks){
      toast(`回验 ${j.ok?"通过":"未通过"} · rem=${j.checks.remediation_ok} alert=${j.checks.alert_quiet}`,"ok");
    } else toast(action+" 完成","ok");
    openIncidentDetail(id); loadRemediation(); loadSREBadge();
  }catch(e){ toast(String(e),"err"); }
}
async function loadIncidentRelatedChanges(id){
  const el=$("incRelatedChanges"); if(!el) return;
  try{
    const list=await fetch(`${API}/incidents/${id}/related-changes`).then(r=>r.json());
    if(!list||!list.length){ el.innerHTML=`<div class="empty-line">${I18N.t("sre.no_related_changes","近 14 天无关联变更")}</div>`; return; }
    el.innerHTML=list.map(c=>`<div class="sre-row"><div class="sre-row-main"><div class="sre-row-title">#${c.id} ${esc(c.title)}</div>
      <div class="sre-row-sub">${esc(c.kind)} · ${esc(c.status)} · ${esc(c.risk)} · ${fmtDateTime(c.started_at)}${c.author?" · "+esc(c.author):""}</div></div></div>`).join("");
  }catch(e){ el.innerHTML=`<div class="empty-line">—</div>`; }
}
async function incidentAction(id, act){
  try {
    if (act==="comment-attach"){ const f=$("incCommentFile"); if(f) f.click(); return; }
    if (act==="comment"){
      const t=($("incCommentInput")&&$("incCommentInput").value||"").trim();
      const atts=window._INC_COMMENT_ATTACHMENTS||[];
      if(!t && !atts.length) return;
      await fetch(`${API}/incidents/${id}/comment`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({text:t,attachments:attachmentsToAPI(atts)})});
      window._INC_COMMENT_ATTACHMENTS=[];
    }
    else if (act==="escalate"){
      const r=await fetch(`${API}/incidents/${id}/ticket`,{method:"POST"});
      const tk=await r.json().catch(()=>({}));
      toast(`${I18N.t("sre.escalated_to_ticket","已升级为工单")} #${tk.id||"?"}`,"ok");
      if (tk && tk.id){ openTicketModal(tk); return; }
    }
    else if (act==="open-ticket"){
      const tid=(window._curIncident&&window._curIncident.ticket_id)||0;
      if(!tid){ toast(I18N.t("sre.ticket_none","尚未升级工单"),"err"); return; }
      const tk=await fetch(`${API}/tickets/${tid}`).then(r=>r.json());
      openTicketModal(tk); return;
    }
    else if (act==="close-ticket"){
      const tid=(window._curIncident&&window._curIncident.ticket_id)||0;
      if(!tid){ toast(I18N.t("sre.ticket_none","尚未升级工单"),"err"); return; }
      const ok = typeof uiConfirm === "function"
        ? await uiConfirm({
            title: I18N.t("ui.close","关闭"),
            message: I18N.t("sre.confirm_close_ticket","关闭工单并回写事件为已解决？"),
            tone: "danger"
          })
        : confirm(I18N.t("sre.confirm_close_ticket","关闭工单并回写事件为已解决？"));
      if(!ok) return;
      const cur=await fetch(`${API}/tickets/${tid}`).then(r=>r.json());
      const body={title:cur.title,priority:cur.priority||"p3",status:"closed",assignee:cur.assignee||"",description:cur.description||""};
      const r=await fetch(`${API}/tickets/${tid}`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
      const j=await r.json().catch(()=>({}));
      if(!r.ok){ toast(j.error||I18N.t("toast.operation_failed","操作失败"),"err"); return; }
      toast(I18N.t("sre.ticket_closed_inc","工单已关闭，关联事件已标记解决"),"ok");
      openIncidentDetail(id); loadIncidents(); loadTickets(); loadSREBadge(); return;
    }
    else if (act==="emergency-change"){
      const r=await fetch(`${API}/incidents/${id}/emergency-change`,{method:"POST",headers:{"Content-Type":"application/json"},body:"{}"});
      const j=await r.json().catch(()=>({}));
      if(!r.ok){ toast(j.error||"创建应急变更失败","err"); return; }
      toast(`已创建应急变更 #${j.id||"?"}`,"ok");
      openChangeRecModal(j); loadChanges(); return;
    }
    else if (act==="link-sr"){
      const tid=prompt("输入要关联的服务请求/工单 ID");
      if(!tid) return;
      const r=await fetch(`${API}/incidents/${id}/link-ticket`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({ticket_id:parseInt(tid,10)||0})});
      const j=await r.json().catch(()=>({}));
      if(!r.ok){ toast(j.error||"关联失败","err"); return; }
      toast(`已关联工单 #${j.id||tid}`,"ok");
      openIncidentDetail(id); loadTickets(); return;
    }
    else if (act==="diagnose"){
      // 流式写入诊断会话（与追问同源 UI），不再丢弃 SSE
      await streamIncidentDiagnose(id);
      return; // 诊断会话已就地更新，勿再 openIncidentDetail 以免清掉流式内容
    }
    else if (act==="draft-rule"){ draftRemediationFromIncident(window._curIncident); return; } // 不走末尾刷新
    else if (act==="propose-fix"){ proposeRemediationFromIncident(window._curIncident); return; }
    else if (act==="topo-rca"){ showIncidentTopoRCA(window._curIncident); return; }
    else if (act==="analysis-board"){
      toast(I18N.t("sre.gen_board_ing","AI 生成分析看板中，请稍候…"),"ok");
      const r=await fetch(`${API}/dashboards/ai-from-incident`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({incident_id:+id})});
      const j=await r.json().catch(()=>({}));
      if(j.ok){ $("incidentDetailMask").classList.remove("show"); toast(`${I18N.t("sre.board_generated","已生成分析看板")}：${j.name}`,"ok"); switchView("dashboards"); if(typeof openDashboard==="function") await openDashboard(j.id); }
      else toast(j.error||I18N.t("toast.operation_failed","操作失败"),"err");
      return; // 不走末尾刷新
    }
    else await fetch(`${API}/incidents/${id}/${act}`,{method:"POST"});
    openIncidentDetail(id); loadIncidents(); loadSREBadge();
  } catch(e){ toast(I18N.t("toast.operation_failed","操作失败")+": "+e,"err"); }
}

// 一键诊断：SSE 写入 #incDiagnosisChat，与诊断追问共用渲染逻辑。
async function streamIncidentDiagnose(id){
  window._incDiagId = id;
  if(!Array.isArray(window._incDiagHistory)) window._incDiagHistory = [];
  const aiMsg={role:"assistant",content:"",_streaming:true,_loading:true};
  window._incDiagHistory.push(aiMsg);
  renderDiagnosisChat();
  const stageLabel={context:"🔍 整理上下文",rag:"📊 检索案例",moa:"🧠 多模型研判",generate:"🤖 生成结论",verify:"🔎 自我校验",done:"✅ 完成"};
  const loadingPhrases=["🔍 "+I18N.t("sre.diag_phase_ctx","正在分析事件上下文…"),"📊 "+I18N.t("sre.diag_phase_similar","检索历史相似案例…"),"🤖 "+I18N.t("sre.diag_phase_think","AI 正在思考…")];
  let loadingIdx=0;
  const loadingTimer=setInterval(()=>{
    loadingIdx=(loadingIdx+1)%loadingPhrases.length;
    if(aiMsg._loading && !aiMsg._stage){ aiMsg.content=loadingPhrases[loadingIdx]; renderDiagnosisChat(); }
  },2000);
  let renderThrottle=null;
  const throttledRender=()=>{
    if(renderThrottle) return;
    renderThrottle=requestAnimationFrame(()=>{ renderThrottle=null; renderDiagnosisChat(); });
  };
  try {
    const r=await fetch(`${API}/incidents/${id}/diagnose`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({stream:true})});
    if(!r.ok) throw new Error("HTTP "+r.status);
    const ct=r.headers.get("content-type")||"";
    if(!ct.includes("event-stream")){
      // 启发式 JSON 回退
      clearInterval(loadingTimer);
      const j=await r.json().catch(()=>({}));
      aiMsg._loading=false; aiMsg._streaming=false;
      const text=j.diagnosis||j.summary||j.reply||"";
      const src=j.source==="heuristic"?I18N.t("sre.heuristic","启发式"):"AI";
      aiMsg.content=text?`【${src}】\n${text}`:(j.error||I18N.t("sre.empty_reply","（空回复）"));
      if(/AI 未配置|未启用/.test(String(j.error||""))) promptOpenAIConfig(j.error);
      renderDiagnosisChat();
      loadIncidents(); loadSREBadge();
      return;
    }
    await readSSEStream(r,
      (delta,fullText)=>{
        if(aiMsg._loading){ clearInterval(loadingTimer); aiMsg._loading=false; }
        aiMsg.content=fullText;
        throttledRender();
      },
      (err)=>{
        clearInterval(loadingTimer); aiMsg._loading=false; aiMsg._streaming=false;
        aiMsg.content="❌ "+err;
        if(/AI 未配置|未启用/.test(String(err||""))) promptOpenAIConfig(err);
        renderDiagnosisChat();
      },
      (fullText)=>{
        clearInterval(loadingTimer); aiMsg._loading=false; aiMsg._streaming=false;
        aiMsg.content=fullText||aiMsg.content||I18N.t("sre.empty_reply","（空回复）");
        if(renderThrottle){ cancelAnimationFrame(renderThrottle); renderThrottle=null; }
        renderDiagnosisChat();
      },
      null,
      (meta)=>{
        applyRAGMetaHint(meta, "incDiagnosisChat");
        if(meta&&meta.stage){
          aiMsg._stage=meta.stage;
          if(aiMsg._loading){
            aiMsg.content=(stageLabel[meta.stage]||meta.label||meta.stage)+"…";
            throttledRender();
          }
        }
      },
      null,
      (rd,fullReasoning)=>{
        if(aiMsg._loading){ clearInterval(loadingTimer); aiMsg._loading=false; }
        aiMsg._reasoning=fullReasoning;
        throttledRender();
      }
    );
  } catch(e){
    clearInterval(loadingTimer);
    aiMsg._loading=false; aiMsg._streaming=false;
    aiMsg.content="❌ "+I18N.t("toast.network_error","网络错误")+": "+e;
    renderDiagnosisChat();
  }
  // 保留本地流式会话内容；仅刷新列表/角标（勿 openIncidentDetail，否则会清掉刚写入的聊天）
  loadIncidents(); loadSREBadge();
}

// AI 未配置时引导打开设置
function promptOpenAIConfig(err){
  const tip=String(err||I18N.t("sre.ai_not_configured","AI 未配置或未启用"));
  if(typeof toast==="function") toast(tip,"err");
  if(typeof openAIConfig!=="function") return;
  setTimeout(()=>{ try{ openAIConfig(); }catch(e){} }, 200);
}

// RAG 降级 / 命中提示 + 证据链卡片（挂到目标容器顶部）
function applyRAGMetaHint(meta, containerId){
  if(!meta) return;
  const tip=meta.degraded_tip||"";
  const hits=[];
  if(typeof meta.memory_hits==="number" && meta.memory_hits>0) hits.push(I18N.t("sre.rag_mem","记忆")+" ×"+meta.memory_hits);
  if(typeof meta.skill_hits==="number" && meta.skill_hits>0){
    let sk=I18N.t("sre.rag_skill","技能")+" ×"+meta.skill_hits;
    if(Array.isArray(meta.skill_names) && meta.skill_names.length){
      sk+="（"+meta.skill_names.slice(0,4).join("、")+(meta.skill_names.length>4?"…":"")+"）";
    }
    hits.push(sk);
  }
  let text=tip;
  if(meta.weknora_degraded && meta.weknora_tip){
    text = text ? (text+" · "+meta.weknora_tip) : meta.weknora_tip;
  }
  if(hits.length) text = (text? text+" · ":"") + "📚 "+hits.join(" · ");
  const host=containerId?document.getElementById(containerId):null;
  if(host){
    if(text){
      let bar=host.querySelector(".ai-rag-hint");
      if(!bar){ bar=document.createElement("div"); bar.className="ai-rag-hint"; host.prepend(bar); }
      bar.textContent=text;
      bar.title=tip||meta.weknora_tip||text;
    }
    if(Array.isArray(meta.citations) && meta.citations.length && typeof renderAssistCitations==="function"){
      let wrap=host.querySelector(".ai-evidence-host");
      if(!wrap){
        wrap=document.createElement("div");
        wrap.className="ai-evidence-host";
        const bar=host.querySelector(".ai-rag-hint");
        if(bar&&bar.nextSibling) host.insertBefore(wrap, bar.nextSibling);
        else if(bar) bar.after(wrap);
        else host.prepend(wrap);
      }
      wrap.innerHTML=renderAssistCitations(meta.citations,{open:true});
      // Keep on the latest assistant bubble for chat re-renders
      const hist=window._incDiagHistory;
      if(Array.isArray(hist)){
        for(let i=hist.length-1;i>=0;i--){
          if(hist[i]&&hist[i].role==="assistant"){ hist[i]._citations=meta.citations.slice(0,12); break; }
        }
      }
    }
  } else if(typeof toast==="function" && (tip||meta.weknora_tip)){
    toast(tip||meta.weknora_tip,"ok");
  }
}

// 闭环：把事件的 AI 诊断建议转成「自动修复规则草稿」。组织上下文（事件+最新诊断+可用剧本）后
// 调用统一 /ai/assist（task=remediation_rule），AI 产出 {playbook?,rule} JSON 供人工确认后落地。
function draftRemediationFromIncident(inc){
  if(!inc){ toast(I18N.t("sre.reopen_incident","请重新打开事件详情后再试"),"err"); return; }
  let diag="";
  const tl=inc.timeline||[];
  for(let i=tl.length-1;i>=0;i--){ if(tl[i].kind==="ai_diagnosis" && tl[i].text){ diag=tl[i].text; break; } }
  if(!diag){ toast(I18N.t("sre.need_diag_first","请先运行「🤖 AI 诊断」，有诊断结论后再转规则"),"err"); return; }
  const pbs=(SRE_PLAYBOOKS||[]).map(p=>`- id=${p.id} 名称=${p.name}${p.description?" 用途="+p.description:""}`).join("\n")||"（暂无已保存剧本，请新建）";
  const ctx=`事件：${inc.title}\n告警类型：${inc.type||"(未知)"}\n级别：${inc.severity}\n主机：${inc.hostname||"(未知)"}\n\nAI 诊断结论：\n${diag}\n\n【可用剧本】\n${pbs}`;
  openAIAssist({
    task:"remediation_rule",
    title:I18N.t("sre.to_rule_title","AI 转自动化规则 · 草稿（需人工审核后启用）"),
    mode:"analyze",
    context:ctx,
    applyLabel:I18N.t("sre.to_rule_apply","创建为草稿规则"),
    applyTo:(text)=>applyRemediationDraft(text)
  });
}

// L4：本事件一次性修复提案 → 待审批 → 批准执行
function proposeRemediationFromIncident(inc){
  if(!inc){ toast(I18N.t("sre.reopen_incident","请重新打开事件详情后再试"),"err"); return; }
  if(!inc.host_id){ toast(I18N.t("sre.propose_need_host","事件未关联主机，无法挂修复提案"),"err"); return; }
  let diag="";
  const tl=inc.timeline||[];
  for(let i=tl.length-1;i>=0;i--){ if(tl[i].kind==="ai_diagnosis" && tl[i].text){ diag=tl[i].text; break; } }
  if(!diag){ toast(I18N.t("sre.need_diag_first","请先运行「🤖 AI 诊断」，有诊断结论后再生成提案"),"err"); return; }
  const pbs=(SRE_PLAYBOOKS||[]).map(p=>`- id=${p.id} 名称=${p.name}${p.description?" 用途="+p.description:""}`).join("\n")||"（暂无已保存剧本，请新建）";
  const ctx=`事件ID：${inc.id}\n事件：${inc.title}\n告警类型：${inc.type||"(未知)"}\n级别：${inc.severity}\n主机：${(typeof HostPicker!=="undefined"&&HostPicker.hostTitle)?HostPicker.hostTitle({hostname:inc.hostname,ip:inc.ip,id:inc.host_id}):(inc.hostname||"未知主机")}\n\nAI 诊断结论：\n${diag}\n\n【可用剧本】\n${pbs}`;
  openAIAssist({
    task:"remediation_proposal",
    title:I18N.t("sre.propose_fix_ai_title","AI 生成修复提案 · 审批后执行"),
    mode:"analyze",
    context:ctx,
    applyLabel:I18N.t("sre.propose_fix_apply","提交待审批"),
    applyTo:(text)=>applyRemediationProposal(inc.id, text)
  });
}
async function applyRemediationProposal(incidentId, text){
  let draft;
  try { draft=JSON.parse(extractFirstCodeBlock(text)||text); }
  catch(e){ toast(I18N.t("sre.bad_json_proposal","AI 输出不是合法 JSON，请重试或手工编写剧本"),"err"); return; }
  try {
    const body={
      title: draft.title||"",
      existing_playbook_id: (draft.existing_playbook_id||"").trim(),
      playbook: draft.playbook||null
    };
    if(!body.existing_playbook_id && !body.playbook) throw new Error(I18N.t("sre.no_usable_pb","AI 未给出可用剧本"));
    const r=await fetch(`${API}/incidents/${incidentId}/remediation-propose`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    const j=await r.json().catch(()=>({}));
    if(!r.ok||!j.ok) throw new Error(j.error||I18N.t("sre.propose_failed","提交提案失败"));
    toast("✅ "+I18N.t("sre.propose_ok","已提交修复提案，请在「自动修复」执行记录中批准后执行"),"ok");
    try{ SRE_PLAYBOOKS=(await fetch(`${API}/playbooks`).then(r=>r.json()))||SRE_PLAYBOOKS; }catch(e){}
    const m=$("incidentDetailMask"); if(m) m.classList.remove("show");
    if(typeof closeAIAssist==="function") closeAIAssist();
    if(typeof switchSRETab==="function") switchSRETab("remediation");
    else if(typeof loadRemediation==="function") loadRemediation();
    loadSREBadge();
  } catch(e){ toast(I18N.t("sre.propose_failed","提交提案失败")+"："+e,"err"); }
}
async function showIncidentTopoRCA(inc){
  if(!inc||!inc.host_id){ toast("事件未关联主机","err"); return; }
  try{
    const j=await fetch(`${API}/topology/rca?incident_id=${inc.id}`).then(r=>r.json());
    const text=j.summary||JSON.stringify(j,null,2);
    const body=$("incidentDetailBody");
    if(body){
      let box=body.querySelector(".topo-rca-box");
      if(!box){ box=document.createElement("div"); box.className="topo-rca-box"; body.prepend(box); }
      box.innerHTML=`<div class="subhead">🧭 ${I18N.t("sre.topo_rca","拓扑 RCA")}</div><pre class="skill-steps" style="white-space:pre-wrap;margin:0 0 12px">${esc(text)}</pre>`;
      box.scrollIntoView({behavior:"smooth",block:"nearest"});
    } else toast(text.slice(0,200),"ok");
  }catch(e){ toast("加载 RCA 失败："+e,"err"); }
}

// 落地草稿：新建剧本(若需要) + 建「停用」规则(require_approval 默认 true)，双保险，绝不自动生效。
async function applyRemediationDraft(text){
  let draft;
  try { draft=JSON.parse(extractFirstCodeBlock(text)||text); }
  catch(e){ toast(I18N.t("sre.bad_json_rule","AI 输出不是合法 JSON，请到「自动修复」手动创建规则"),"err"); return; }
  try {
    let playbookId=(draft.existing_playbook_id||"").trim();
    if(!playbookId && draft.playbook){
      const pb=draft.playbook; pb.id="";
      const r=await fetch(`${API}/playbooks`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(pb)});
      const j=await r.json().catch(()=>({}));
      if(!r.ok||!j.id) throw new Error(j.error||I18N.t("sre.create_fix_pb_failed","创建修复剧本失败"));
      playbookId=j.id;
    }
    if(!playbookId) throw new Error(I18N.t("sre.no_usable_pb","AI 未给出可用剧本"));
    const rule=draft.rule||{};
    rule.id=""; rule.playbook_id=playbookId;
    rule.enabled=false; // 关键：草稿默认「停用」，绝不自动触发；人工审核后手动启用即生效
    if(rule.require_approval===undefined) rule.require_approval=true; // 双保险：即便启用也先排队人工审批
    const rr=await fetch(`${API}/remediation/rules`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(rule)});
    const rj=await rr.json().catch(()=>({}));
    if(!rr.ok) throw new Error(rj.error||I18N.t("sre.create_rule_failed","创建规则失败"));
    toast("✅ "+I18N.t("sre.draft_rule_created","已创建『停用』草稿规则，请在「自动修复」审核命令与匹配条件后再启用"),"ok");
    const m=$("incidentDetailMask"); if(m) m.classList.remove("show");
    if(typeof switchSRETab==="function"){ switchSRETab("remediation"); }
    else if(typeof loadRemediation==="function"){ loadRemediation(); }
  } catch(e){ toast(I18N.t("sre.draft_apply_failed","落地草稿失败")+"："+e,"err"); }
}
// ---- AI 诊断多轮对话 ----
// readSSEStream reads a Server-Sent Events stream from a fetch response and
// calls onDelta for each token chunk, onError for errors, onResult for result
// metadata, and onDone when complete. Returns the accumulated full text.
async function readSSEStream(resp,onDelta,onError,onDone,onResult,onMeta,onTool,onReasoning,options){
  const reader=resp.body.getReader();
  const decoder=new TextDecoder();
  const idleTimeoutMs=Math.max(10000,Number(options&&options.idleTimeoutMs)||180000);
  let buf="";
  let fullText="";
  let fullReasoning="";
  const readNext=async()=>{
    let timer=null;
    try{
      return await Promise.race([
        reader.read(),
        new Promise((_,reject)=>{
          timer=setTimeout(()=>reject(new Error(I18N.t("sre.stream_idle_timeout","AI 流式响应超时，请重试"))),idleTimeoutMs);
        })
      ]);
    }finally{
      if(timer) clearTimeout(timer);
    }
  };
  try {
    while(true){
      const {done,value}=await readNext();
      if(done) break;
      buf+=decoder.decode(value,{stream:true});
      // Split by double newlines to get SSE events
      const parts=buf.split("\n\n");
      buf=parts.pop()||"";
      for(const p of parts){
        const lines=p.split("\n");
        for(const line of lines){
          if(!line.startsWith("data: ")) continue;
          const data=line.slice(6);
          if(data==="[DONE]"){ if(onDone) onDone(fullText); return fullText; }
          try {
            const j=JSON.parse(data);
            if(j.error){ if(onError) onError(j.error); return fullText; }
            if(j.meta!==undefined){ if(onMeta) onMeta(Object.assign({}, j.meta, j.session_id!==undefined?{session_id:j.session_id}:{})); continue; }
            if(j.session_id!==undefined){ if(onMeta) onMeta(j); continue; }
            if(j.stage!==undefined){ if(onMeta) onMeta({stage:j.stage,label:j.label||j.stage}); if(options&&typeof options.onStage==="function") options.onStage(j.stage,j.label||j.stage); continue; }
            if(j.result){ if(onResult) onResult(j.result); continue; }
            if(j.tool){ if(onTool) onTool(j.tool); continue; } // 工具执行状态帧（run/ok/err）
            if(j.action){ if(options&&typeof options.onAction==="function") options.onAction(j.action); continue; } // 能力工具 UI 动作卡
            if(j.reasoning!==undefined){ fullReasoning+=j.reasoning; if(onReasoning) onReasoning(j.reasoning,fullReasoning); continue; } // 推理模型思维链增量
            if(j.delta){ fullText+=j.delta; if(onDelta) onDelta(j.delta,fullText); }
          } catch(e){ /* skip malformed chunks */ }
        }
      }
    }
  } catch(e) {
    try { await reader.cancel(String(e||"stream cancelled")); } catch(_e) {}
    throw e;
  } finally { reader.releaseLock(); }
  if(onDone) onDone(fullText);
  return fullText;
}
// 渲染「🧠 思考过程」可折叠区块：默认折叠、暗色弱化，与正文答案视觉分离。
// streaming=true 时自动展开并显示光标，便于用户实时看到推理；完成后可手动收起。
function renderReasoningBlock(reasoning,streaming){
  if(!reasoning) return "";
  const cursor=streaming?'<span class="ai-stream-cursor">▍</span>':"";
  return `<details class="ai-reasoning"${streaming?" open":""}><summary class="ai-reasoning-sum">🧠 ${I18N.t("sre.thinking_process","思考过程")}</summary>`
    +`<div class="ai-reasoning-body">${esc(reasoning)}${cursor}</div></details>`;
}
async function loadDiagnosisChatHistory(incidentId){
  const el=$("incDiagnosisChat"); if(!el) return;
  try {
    const r=await fetch(`${API}/incidents/${incidentId}/diagnose-chat`);
    const j=await r.json();
    window._incDiagHistory = (j.history||[]).map(m=>({role:m.role,content:m.content}));
  } catch(e){ window._incDiagHistory=[]; }
  renderDiagnosisChat();
}
function renderDiagnosisChat(){
  const el=$("incDiagnosisChat"); if(!el) return;
  const hist=window._incDiagHistory||[];
  if(!hist.length){ el.innerHTML=`<div class="empty-line" style="padding:12px">${I18N.t("sre.diag_chat_empty","点击下方「🤖 AI 诊断」获取初步研判，然后在此追问细节。")}</div>`; return; }
  el.innerHTML=hist.map((m,i)=>{
    const cls=m.role==="user"?"me":m.role==="assistant"?"ai":"sys";
    // 思维链折叠区（推理模型）：流式中展开、完成后收起；无思维链时返回空串
    const rb=(m.role==="assistant")?renderReasoningBlock(m._reasoning,!!m._streaming):"";
    const cites=(m.role==="assistant"&&m._citations&&typeof renderAssistCitations==="function")
      ? renderAssistCitations(m._citations,{open:!m._streaming}) : "";
    let body;
    if(m.role==="assistant" && m._streaming && m._loading){
      // 等待 AI 响应：显示动态加载提示（此时可能已在流式接收思维链）
      body=rb+`<div class="ai-thinking"><span class="ai-thinking-dots"><span></span><span></span><span></span></span> <span class="ai-thinking-text">${esc(m.content||I18N.t("sre.analyzing","正在分析…"))}</span></div>`+cites;
    } else if(m.role==="assistant" && m._streaming){
      // 流式中：显示纯文本 + 闪烁光标，避免未完成 Markdown 导致渲染抖动
      body=rb+`<span class="ai-stream-text">${esc(m.content||"")}</span><span class="ai-stream-cursor">▍</span>`+cites;
    } else if(m.role==="assistant" && m.content!=="思考中…" && !m.content.startsWith("❌")){
      body=rb+renderAIMarkdown(filterDisplayContent(m.content||""))+cites;
    } else {
      body=esc(m.content)+cites;
    }
    let fb="";
    if(m.role==="assistant" && m.content!=="思考中…" && !m._streaming){
      fb=m._feedback
        ? `<div class="ai-chat-fb"><span class="badge ok">${m._feedback==="helpful"?"👍 "+I18N.t("sre.helpful","有用"):"👎 "+I18N.t("sre.unhelpful","无用")}</span></div>`
        : `<div class="ai-chat-fb"><button class="btn-tiny" data-fb="helpful" data-idx="${i}" title="${I18N.t("sre.helpful","有用")}">👍</button><button class="btn-tiny" data-fb="unhelpful" data-idx="${i}" title="${I18N.t("sre.unhelpful","无用")}">👎</button></div>`;
    }
    return `<div class="ai-chat-msg ${cls}">${body}${fb}</div>`;
  }).join("");
  // Wire feedback buttons
  el.querySelectorAll("[data-fb]").forEach(b=>b.onclick=()=>sendDiagnosisFeedback(parseInt(b.dataset.idx),b.dataset.fb==="helpful"));
  el.querySelectorAll(".ai-chat-msg.ai").forEach(d=>addCopyTool(d,d.textContent,{feedback:false,regenerate:false}));
  el.scrollTop=el.scrollHeight;
}
async function sendDiagnosisFeedback(idx,helpful){
  if(!window._incDiagId) return;
  let reason="";
  if(!helpful){
    reason=await requestAIFeedbackReason({
      title:I18N.t("sre.improve_ai","帮助 AI 改进"),
      message:I18N.t("sre.unhelpful_reason","请简要说明为何无用（将写入避坑记忆）：")
    });
    if(reason===null) return;
    reason=(reason||"").trim();
    if(!reason){ toast(I18N.t("sre.need_unhelpful_reason","差评需填写原因"),"err"); return; }
  }
  try {
    const history=window._incDiagHistory||[];
    const answer=(history[idx]&&history[idx].content)||"";
    let input="";
    for(let i=idx-1;i>=0;i--){ if(history[i]&&history[i].role==="user"){ input=history[i].content||""; break; } }
    const r=await fetch(`${API}/incidents/${window._incDiagId}/diagnosis-feedback`,{
      method:"POST",headers:{"Content-Type":"application/json"},
      body:JSON.stringify({message_index:idx,helpful,reason,input,answer})
    });
    const j=await r.json().catch(()=>({}));
    if(!r.ok){ toast(j.error||I18N.t("sre.feedback_failed","反馈失败"),"err"); return; }
    if(history[idx]) history[idx]._feedback=helpful?"helpful":"unhelpful";
    renderDiagnosisChat();
    const learned=j.learning_queued!==false;
    toast(learned
      ? (helpful?I18N.t("sre.marked_helpful","已标记为有用 👍"):I18N.t("sre.marked_unhelpful","已标记为无用 👎"))
      : I18N.t("assist.fb_recorded_no_memory","反馈已记录；持久记忆不可用，本次未进入跨会话学习"), learned?"ok":"warn");
  } catch(e){
    toast(I18N.t("sre.feedback_not_saved","反馈未保存，请检查网络后重试"),"err");
  }
}
async function sendDiagnosisChatMsg(){
  const el=$("incDiagInput"); if(!el) return;
  const msg=el.value.trim();
  const atts=(window._INC_DIAG_ATTACHMENTS||[]).slice();
  if(!msg && !atts.length) return;
  const chat=$("incDiagnosisChat");
  // Show user message immediately (with attachment note)
  const imgN=atts.filter(a=>a.kind==="image").length, fileN=atts.filter(a=>a.kind==="file").length;
  const attNote=atts.length?` 📎 ${imgN?imgN+" "+I18N.t("sre.unit_images","图")+" ":""}${fileN?fileN+" "+I18N.t("sre.unit_files","文件"):""}`:"";
  window._incDiagHistory.push({role:"user",content:msg||(I18N.t("sre.attachment_only","（附件）")+attNote)});
  renderDiagnosisChat();
  el.value=""; el.disabled=true; $("incDiagSendBtn").disabled=true;
  window._INC_DIAG_ATTACHMENTS=[]; renderDiagAttachments();
  // Add a placeholder for AI response with animated loading
  const aiMsg={role:"assistant",content:"",_streaming:true,_loading:true};
  window._incDiagHistory.push(aiMsg);
  renderDiagnosisChat();
  // 动画加载提示
  const loadingPhrases=["🔍 "+I18N.t("sre.diag_phase_ctx","正在分析事件上下文…"),"📊 "+I18N.t("sre.diag_phase_similar","检索历史相似案例…"),"🤖 "+I18N.t("sre.diag_phase_think","AI 正在思考…")];
  let loadingIdx=0;
  const loadingTimer=setInterval(()=>{
    loadingIdx=(loadingIdx+1)%loadingPhrases.length;
    if(aiMsg._loading){ aiMsg.content=loadingPhrases[loadingIdx]; renderDiagnosisChat(); }
  },2000);
  try {
    const cleanHist=window._incDiagHistory.filter(m=>!m._streaming&&m.content!=="思考中…").map(m=>({role:m.role,content:m.content}));
    const images=atts.filter(a=>a.kind==="image").map(a=>({mime:a.mime,data:a.data}));
    const files=atts.filter(a=>a.kind==="file").map(a=>({name:a.name,text:a.text}));
    const r=await fetch(`${API}/incidents/${window._incDiagId}/diagnose-chat`,{
      method:"POST",headers:{"Content-Type":"application/json"},
      body:JSON.stringify({message:msg,history:cleanHist,include_terminal:!!$("incTermCheck")?.checked,stream:true,images,files})
    });
    if(!r.ok){ throw new Error("HTTP "+r.status); }
    // SSE streaming
    let renderThrottle=null;
    const throttledRender=()=>{
      if(renderThrottle) return;
      renderThrottle=requestAnimationFrame(()=>{ renderThrottle=null; renderDiagnosisChat(); });
    };
    await readSSEStream(r,
      (delta,fullText)=>{
        if(aiMsg._loading){ clearInterval(loadingTimer); aiMsg._loading=false; }
        aiMsg.content=fullText;
        throttledRender();
      },
      (err)=>{
        clearInterval(loadingTimer); aiMsg._loading=false; aiMsg._streaming=false;
        aiMsg.content="❌ "+err;
        if(/AI 未配置|未启用/.test(String(err||""))) promptOpenAIConfig(err);
        renderDiagnosisChat();
      },
      (fullText)=>{
        clearInterval(loadingTimer); aiMsg._loading=false;
        aiMsg._streaming=false;
        aiMsg.content=fullText||aiMsg.content||I18N.t("sre.empty_reply","（空回复）");
        if(renderThrottle){ cancelAnimationFrame(renderThrottle); renderThrottle=null; }
        renderDiagnosisChat();
      },
      null, // onResult
      (meta)=>{ applyRAGMetaHint(meta, "incDiagnosisChat"); }, // onMeta
      null, // onTool
      (rd,fullReasoning)=>{ // 思维链增量：累积到 aiMsg._reasoning 并实时渲染
        if(aiMsg._loading){ clearInterval(loadingTimer); aiMsg._loading=false; }
        aiMsg._reasoning=fullReasoning;
        throttledRender();
      }
    );
  } catch(e){
    clearInterval(loadingTimer);
    aiMsg._loading=false; aiMsg._streaming=false;
    aiMsg.content="❌ "+I18N.t("toast.network_error","网络错误")+": "+e;
    renderDiagnosisChat();
  }
  el.disabled=false; $("incDiagSendBtn").disabled=false; el.focus();
}
// Req1: 诊断对话附件渲染与文件处理（复用主对话的附件逻辑）
function renderDiagAttachments(){
  const box=$("incDiagAttach"); if(!box) return;
  const atts=window._INC_DIAG_ATTACHMENTS||[];
  if(!atts.length){ box.innerHTML=""; box.style.display="none"; return; }
  box.style.display="flex";
  box.innerHTML=atts.map((a,i)=>`<span class="ai-attach-chip">${a.kind==="image"?"🖼️":"📄"} ${esc(a.name)}<button data-datt="${i}" title="${I18N.t("sre.remove","移除")}">✕</button></span>`).join("");
  box.querySelectorAll("[data-datt]").forEach(b=>b.onclick=()=>{ window._INC_DIAG_ATTACHMENTS.splice(parseInt(b.dataset.datt),1); renderDiagAttachments(); });
}
function onDiagChatFiles(ev){
  const files=Array.from((ev.target&&ev.target.files)||[]);
  if(!window._INC_DIAG_ATTACHMENTS) window._INC_DIAG_ATTACHMENTS=[];
  for(const f of files){
    if(f.type&&f.type.startsWith("image/")){
      if(window._INC_DIAG_ATTACHMENTS.filter(a=>a.kind==="image").length>=4){ if(typeof toast==="function") toast(I18N.t("sre.max_4_images","最多 4 张图片"),"err"); continue; }
      if(f.size>4*1024*1024){ if(typeof toast==="function") toast(`${I18N.t("sre.image","图片")} ${f.name} ${I18N.t("sre.exceeds_4mb","超过 4MB")}`,"err"); continue; }
      const rd=new FileReader();
      rd.onload=()=>{ const s=String(rd.result||""); const c=s.indexOf(","); window._INC_DIAG_ATTACHMENTS.push({kind:"image",name:f.name,mime:f.type||"image/png",data:c>=0?s.slice(c+1):s}); renderDiagAttachments(); };
      rd.readAsDataURL(f);
    } else if(_AI_PARSE_EXT.includes(_extOf(f.name))){
      if(f.size>10*1024*1024){ if(typeof toast==="function") toast(`${I18N.t("sre.file","文件")} ${f.name} ${I18N.t("sre.exceeds_10mb","超过 10MB")}`,"err"); continue; }
      parseDiagFileAttachment(f);
    } else {
      if(f.size>1024*1024){ if(typeof toast==="function") toast(`${I18N.t("sre.file","文件")} ${f.name} ${I18N.t("sre.exceeds_1mb","超过 1MB")}`,"err"); continue; }
      const rd=new FileReader();
      rd.onload=()=>{ window._INC_DIAG_ATTACHMENTS.push({kind:"file",name:f.name,text:String(rd.result||"")}); renderDiagAttachments(); };
      rd.readAsText(f);
    }
  }
  if(ev.target) ev.target.value="";
}
function parseDiagFileAttachment(f){
  const rd=new FileReader();
  rd.onload=async()=>{
    const s=String(rd.result||""); const c=s.indexOf(","); const b64=c>=0?s.slice(c+1):s;
    const ph={kind:"file",name:f.name,text:I18N.t("sre.parsing","（解析中…）")};
    if(!window._INC_DIAG_ATTACHMENTS) window._INC_DIAG_ATTACHMENTS=[];
    window._INC_DIAG_ATTACHMENTS.push(ph); renderDiagAttachments();
    try{
      const r=await fetch(`${API}/hermes/parse`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({name:f.name,mime:f.type||"",data:b64})});
      const j=await r.json().catch(()=>({}));
      if(!r.ok||j.error){ window._INC_DIAG_ATTACHMENTS=window._INC_DIAG_ATTACHMENTS.filter(a=>a!==ph); if(typeof toast==="function") toast(`${I18N.t("sre.parse_v","解析")} ${f.name} ${I18N.t("sre.failed_v","失败")}`,"err"); renderDiagAttachments(); return; }
      ph.text=j.text||""; renderDiagAttachments();
      if(typeof toast==="function") toast(`${I18N.t("sre.parsed_v","已解析")} ${f.name}（${j.chars||0} ${I18N.t("sre.chars_unit","字")}）`,"ok");
    }catch(e){ window._INC_DIAG_ATTACHMENTS=window._INC_DIAG_ATTACHMENTS.filter(a=>a!==ph); if(typeof toast==="function") toast(`${I18N.t("sre.parse_v","解析")} ${f.name} ${I18N.t("sre.failed_v","失败")}`,"err"); renderDiagAttachments(); }
  };
  rd.readAsDataURL(f);
}
function openNewIncident(){
  $("niTitle").value=""; $("niSeverity").value="warning";
  $("niHost").innerHTML=`<option value="">—</option>`+SRE_HOSTS.map(h=>`<option value="${esc(h.id)}">${esc((window.HostPicker&&HostPicker.optionLabel)?HostPicker.optionLabel(h):(h.hostname||h.id))}</option>`).join("");
  $("newIncidentMask").classList.add("show");
}
async function saveNewIncident(){
  const title=$("niTitle").value.trim(); if(!title){ toast(I18N.t("sre.fill_title","请填写标题"),"err"); return; }
  await fetch(`${API}/incidents`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({title,severity:$("niSeverity").value,host_id:$("niHost").value})});
  $("newIncidentMask").classList.remove("show"); loadIncidents(); loadSREBadge(); toast(I18N.t("toast.saved","已保存"),"ok");
}

/* ---- 自动修复 ---- */
async function loadRemediation(){
  try {
    const [rules,runs] = await Promise.all([fetch(`${API}/remediation/rules`).then(r=>r.json()),fetch(`${API}/remediation/runs`).then(r=>r.json())]);
    SRE_RULES = rules||[]; renderRules(SRE_RULES); renderRuns(runs||[]);
  } catch(e){ toast(I18N.t("sre.load_failed","加载失败")+": "+e,"err"); }
}
function renderRules(rules){
  const el=$("remediationRuleList");
  if(!rules.length){ el.innerHTML=`<div class="empty-line">${I18N.t("sre.no_rules","暂无修复规则")}</div>`; return; }
  el.innerHTML = rules.map(r=>{
    const pb=SRE_PLAYBOOKS.find(p=>p.id===r.playbook_id);
    const g=[]; if(r.dry_run)g.push(I18N.t("sre.badge_dry_run","仅演练")); if(r.require_approval)g.push(I18N.t("sre.badge_need_approval","需审批")); if(r.cooldown_sec)g.push(`${I18N.t("sre.badge_cooldown","冷却")}${r.cooldown_sec}s`); if(r.max_per_hour)g.push(`≤${r.max_per_hour}/h`); if(r.rollback_playbook_id)g.push(I18N.t("sre.badge_rollback","含回滚"));
    const match=(r.match_types&&r.match_types.length?r.match_types.join("/"):I18N.t("sre.any_type","任意类型"))+(r.min_level?` ≥${r.min_level}`:"");
    return `<div class="pb-card fwd-card ${r.enabled?"":"pb-off"}" data-rule="${esc(r.id)}">
      <div class="pb-card-top"><div class="pb-card-title"><strong>${esc(r.name)}</strong><span class="pb-desc">${esc(match)} → ${esc(pb?pb.name:r.playbook_id)}</span></div>
        <span class="fwd-status ${r.enabled?"on":"off"}">${r.enabled?I18N.t("sre.enabled_state","已启用"):I18N.t("sre.disabled_state","已停用")}</span></div>
      <div class="pb-card-foot"><div class="pb-pills">${g.map(x=>`<span class="badge">${esc(x)}</span>`).join("")}</div>
        <div class="fwd-actions"><button class="btn sm" data-rract="edit">${I18N.t("ui.edit","编辑")}</button><button class="btn danger sm" data-rract="del">${I18N.t("ui.delete","删除")}</button></div></div></div>`;
  }).join("");
  el.querySelectorAll("[data-rule]").forEach(card=>card.querySelectorAll("[data-rract]").forEach(b=>b.onclick=async e=>{ e.stopPropagation();
    const id=card.dataset.rule;
    if(b.dataset.rract==="edit") openRuleModal(SRE_RULES.find(x=>x.id===id));
    else {
      const ok = typeof uiConfirm === "function"
        ? await uiConfirm({ title: I18N.t("ui.delete","删除"), message: I18N.t("sre.confirm_del_rule","确认删除该规则？"), tone: "danger" })
        : confirm(I18N.t("sre.confirm_del_rule","确认删除该规则？"));
      if(ok) fetch(`${API}/remediation/rules/${id}`,{method:"DELETE"}).then(()=>loadRemediation());
    }
  }));
}
function renderRuns(runs){
  const el=$("remediationRunList");
  if(!runs.length){ el.innerHTML=`<div class="empty-line">${I18N.t("sre.no_runs","暂无执行记录")}</div>`; return; }
  el.innerHTML = runs.map(r=>{
    const isProposal=!r.rule_id || r.alert_type==="proposal";
    const freeze=/变更冻结/.test(String(r.reason||""));
    const title=isProposal?(`${esc(r.rule_name||I18N.t("sre.proposal","修复提案"))} → ${esc(r.playbook_name||r.playbook_id)}`):(`${esc(r.rule_name)} → ${esc(r.playbook_name||r.playbook_id)}`);
    const subBits=[esc(r.hostname), isProposal?I18N.t("sre.proposal_once","一次性提案"):esc(r.alert_type), fmtDateTime(r.created_at)];
    if(r.reason && !String(r.reason).startsWith("proposed_by:")) subBits.push(esc(r.reason));
    return `<div class="sre-row">
    <span class="badge ${_runCls(r.status)}">${_runStatus(r.status)}</span>
    <div class="sre-row-main"><div class="sre-row-title">${title}${isProposal?` <span class="badge info">${I18N.t("sre.proposal","提案")}</span>`:""}${freeze?` <span class="badge freeze" title="${esc(r.reason||"")}">${I18N.t("sre.freeze_badge","冻结中")}</span>`:""}</div>
      <div class="sre-row-sub">${subBits.join(" · ")}</div></div>
    ${r.status==="pending_approval"?`<div class="fwd-actions"><button class="btn primary sm" data-run="${r.id}" data-runact="approve">${I18N.t("sre.approve","批准")}</button><button class="btn danger sm" data-run="${r.id}" data-runact="reject">${I18N.t("sre.reject","拒绝")}</button></div>`:""}</div>`;
  }).join("");
  el.querySelectorAll("[data-runact]").forEach(b=>b.onclick=async()=>{ await fetch(`${API}/remediation/runs/${b.dataset.run}/${b.dataset.runact}`,{method:"POST"}); loadRemediation(); loadSREBadge(); });
}

/* ---- 依赖拓扑 ---- */
async function loadTopology(){
  const el=$("topoEdgeList");
  if(!el) return;
  try{
    if(!SRE_HOSTS.length){
      try{ SRE_HOSTS=(await fetch(`${API}/hosts`).then(r=>r.json()))||[]; }catch(e){}
    }
    const edges=await fetch(`${API}/topology/edges`).then(r=>r.json());
    if(!edges||!edges.length){
      el.innerHTML=`<div class="empty-line">暂无依赖边。示例：<code>svc:api</code> depends_on <code>host:&lt;id&gt;</code>，或 <code>cat:DB</code> talks_to <code>cat:App</code>。</div>`;
    } else {
      el.innerHTML=edges.map(e=>`<div class="pb-card fwd-card" data-topo="${esc(e.id)}">
        <div class="pb-card-top"><div class="pb-card-title"><strong class="mono">${esc(e.from)}</strong>
          <span class="pb-desc">— ${esc(e.kind||"depends_on")} →</span>
          <strong class="mono">${esc(e.to)}</strong></div></div>
        <div class="pb-card-foot"><div class="pb-pills">${e.note?`<span class="badge">${esc(e.note)}</span>`:""}</div>
          <div class="fwd-actions"><button class="btn danger sm" data-topo-del="${esc(e.id)}">${I18N.t("ui.delete","删除")}</button></div></div></div>`).join("");
      el.querySelectorAll("[data-topo-del]").forEach(b=>b.onclick=async()=>{
        if(!confirm("删除该依赖边？")) return;
        await fetch(`${API}/topology/edges/${b.dataset.topoDel}`,{method:"DELETE"});
        loadTopology();
      });
    }
    const sel=$("topoRcaHost");
    if(sel){
      const hosts=SRE_HOSTS||[];
      const cur=sel.value;
      sel.innerHTML=`<option value="">选择主机…</option>`+hosts.map(h=>`<option value="${esc(h.id)}">${esc((window.HostPicker&&HostPicker.optionLabel)?HostPicker.optionLabel(h):(h.hostname||h.id))}</option>`).join("");
      if(cur) sel.value=cur;
    }
  }catch(e){ el.innerHTML=`<div class="empty-line">${I18N.t("sre.load_failed","加载失败")}：${esc(String(e))}</div>`; }
}
async function addTopologyEdge(){
  const from=await requestAITextInput({
    title:"添加拓扑依赖",message:"定义依赖起点，支持主机、分类或服务节点。",
    label:"From 节点",placeholder:"host:<id> / cat:<分类> / svc:<服务名>",
    submitLabel:"下一步",danger:false,rows:2,maxLength:300,requiredMessage:"请输入 From 节点"
  });
  if(from===null) return;
  const to=await requestAITextInput({
    title:"添加拓扑依赖",message:`起点：${from}`,
    label:"To 节点",placeholder:"host:<id> / cat:<分类> / svc:<服务名>",
    submitLabel:"下一步",danger:false,rows:2,maxLength:300,requiredMessage:"请输入 To 节点"
  });
  if(to===null) return;
  const kind=await requestAITextInput({
    title:"选择依赖类型",message:`${from} → ${to}`,
    label:"边类型",placeholder:"depends_on | runs_on | talks_to",defaultValue:"depends_on",
    submitLabel:"下一步",danger:false,rows:1,maxLength:32
  });
  if(kind===null) return;
  if(!["depends_on","runs_on","talks_to"].includes(kind)){
    toast("边类型仅支持 depends_on、runs_on 或 talks_to","err");
    return;
  }
  const note=await requestAITextInput({
    title:"补充依赖说明",message:`${from} — ${kind} → ${to}`,
    label:"备注（可选）",placeholder:"例如：API 读取订单数据库",
    submitLabel:"保存依赖",danger:false,rows:3,maxLength:500,required:false
  });
  if(note===null) return;
  try{
    const r=await fetch(`${API}/topology/edges`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({from,to,kind,note})});
    const j=await r.json().catch(()=>({}));
    if(!r.ok||!j.ok) throw new Error(j.error||"保存失败");
    toast("已添加依赖边","ok");
    loadTopology();
  }catch(e){ toast("添加失败："+e,"err"); }
}
async function runTopologyRcaDemo(){
  const hostId=($("topoRcaHost")&&$("topoRcaHost").value)||"";
  if(!hostId){ toast("请先选择主机","err"); return; }
  const out=$("topoRcaOut"); if(out) out.textContent="计算中…";
  try{
    const j=await fetch(`${API}/topology/rca?host_id=${encodeURIComponent(hostId)}`).then(r=>r.json());
    if(out) out.textContent=j.summary||JSON.stringify(j,null,2);
  }catch(e){ if(out) out.textContent="失败："+e; }
}
async function applyAutoTopology(){
  if(!confirm("从 K8s / Hyper-V / Compose 库存自动发现依赖边并合并？已有同向手工边不会被覆盖。")) return;
  try{
    const r=await fetch(`${API}/topology/auto-discover?apply=1`,{method:"POST"});
    const j=await r.json().catch(()=>({}));
    if(!r.ok) throw new Error(j.error||"发现失败");
    toast(`自动拓扑：候选 ${j.count||0}，新增 ${j.added||0}`,"ok");
    loadTopology();
  }catch(e){ toast("自动拓扑失败："+e,"err"); }
}
safeAddEventListener("topoAddBtn","click",addTopologyEdge);
safeAddEventListener("topoAutoBtn","click",applyAutoTopology);
safeAddEventListener("topoRcaBtn","click",runTopologyRcaDemo);
safeAddEventListener("bizSvcAddBtn","click",()=>editBusinessService(null));
safeAddEventListener("bizSvcRefreshBtn","click",loadBusinessServices);
safeAddEventListener("sreEffectRefreshBtn","click",()=>{ loadSREEffect(); loadAIRuns(); });

async function loadBusinessServices(){
  const el=$("bizSvcList"); if(!el) return;
  try{
    const list=await fetch(`${API}/services`).then(r=>r.json());
    if(!list||!list.length){ el.innerHTML=`<div class="empty-line">暂无业务服务，点击「+ 业务服务」创建</div>`; return; }
    el.innerHTML=list.map(s=>`<div class="fwd-card">
      <div class="fwd-card-title">${esc(s.name||s.id)} ${s.env?`<span class="badge">${esc(s.env)}</span>`:""}</div>
      <div class="fwd-card-sub">${esc(s.owner||"—")} · hosts ${(s.host_ids||[]).length} · ds ${(s.datasource_ids||[]).length}</div>
      <div class="fwd-card-acts">
        <button class="btn sm" data-bs="impact" data-id="${esc(s.id)}">影响面</button>
        <button class="btn sm" data-bs="edit" data-id="${esc(s.id)}">编辑</button>
        <button class="btn danger sm" data-bs="del" data-id="${esc(s.id)}">删除</button>
      </div></div>`).join("");
    el.querySelectorAll("[data-bs]").forEach(b=>b.onclick=()=>bizSvcAct(b.dataset.bs,b.dataset.id,list));
  }catch(e){ el.innerHTML=`<div class="empty-line">加载失败：${esc(String(e))}</div>`; }
}
async function bizSvcAct(act,id,list){
  if(act==="del"){
    if(!confirm("删除业务服务？")) return;
    await fetch(`${API}/services/${encodeURIComponent(id)}`,{method:"DELETE"});
    loadBusinessServices(); return;
  }
  if(act==="edit"){
    editBusinessService((list||[]).find(x=>x.id===id)||null); return;
  }
  if(act==="impact"){
    const j=await fetch(`${API}/services/${encodeURIComponent(id)}/impact`).then(r=>r.json());
    const lines=[
      `服务：${j.service&&j.service.name||id}`,
      `主机：${(j.hosts||[]).join(", ")||"—"}`,
      `未决事件：${(j.open_incidents||[]).length}`,
      ...(j.open_incidents||[]).slice(0,8).map(i=>`  #${i.id} ${i.title} (${i.severity})`),
      `近14天变更：${(j.recent_changes||[]).length}`,
      ...(j.recent_changes||[]).slice(0,5).map(c=>`  CHG#${c.id} ${c.title} [${c.status}]`),
    ];
    alert(lines.join("\n"));
  }
}
function editBusinessService(svc){
  const name=prompt("服务名称", svc&&svc.name||"");
  if(!name) return;
  const owner=prompt("负责人", svc&&svc.owner||"")||"";
  const env=prompt("环境 prod/staging/dev", svc&&svc.env||"prod")||"prod";
  const hosts=(prompt("主机 ID（逗号分隔）", (svc&&svc.host_ids||[]).join(","))||"").split(",").map(s=>s.trim()).filter(Boolean);
  const body={id:svc&&svc.id||undefined,name,owner,env,host_ids:hosts};
  fetch(`${API}/services`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)})
    .then(r=>r.json()).then(j=>{ if(j.error) toast(j.error,"err"); else { toast("已保存","ok"); loadBusinessServices(); }})
    .catch(e=>toast(String(e),"err"));
}

function _fmtDur(sec){
  sec=Number(sec)||0;
  if(sec<60) return sec+"s";
  if(sec<3600) return Math.round(sec/60)+"m";
  return (sec/3600).toFixed(1)+"h";
}
async function loadSREEffect(){
  const el=$("sreEffectBody"); if(!el) return;
  try{
    const j=await fetch(`${API}/sre/effect?days=14`).then(r=>r.json());
    const card=(title,val,sub)=>`<div class="sre-kpi"><div class="sre-kpi-v">${val}</div><div class="sre-kpi-t">${title}</div>${sub?`<div class="hint">${sub}</div>`:""}</div>`;
    el.innerHTML=`
      <div class="sre-kpi-grid">
        ${card("MTTR P50/P75/P90", `${_fmtDur(j.mttr_p50_sec)} / ${_fmtDur(j.mttr_p75_sec)} / ${_fmtDur(j.mttr_p90_sec)}`, `MTTA P50 ${_fmtDur(j.mtta_p50_sec)}`)}
        ${card("告警噪声比", (j.alert_noise_ratio||0).toFixed(2), `复开 ${j.alert_reopen_keys||0} · 抖动 ${j.alert_flap_keys||0}`)}
        ${card("变更失败率", `${(100*(j.change_failure_rate||0)).toFixed(0)}%`, `${j.change_failed_count||0}/${j.change_count||0} · Lead P75 ${_fmtDur(j.change_lead_time_p75_sec)}`)}
        ${card("AI 采纳 / 验证", `${(100*(j.ai_adoption_rate||0)).toFixed(0)}% / ${(100*(j.verify_pass_rate||0)).toFixed(0)}%`, `反馈 ${j.ai_helpful_count||0}/${j.ai_feedback_count||0} · verify n=${j.verify_sample_size||0}`)}
        ${card("闭环完成率", `${(100*(j.closed_loop_rate||0)).toFixed(0)}%`, `回验通过 ${j.closed_loop_count||0} / 事件 ${j.incident_count||0}`)}
        ${card("Agent 可观测", `工具轮 ${j.ai_tool_turn_runs||0}`, `Fallback ${j.ai_fallback_count||0} · Runs ${j.ai_run_count||0}`)}
        ${card("学习资产", `Skill 命中 ${j.skill_hit_runs||0}`, `记忆命中 ${j.memory_hit_runs||0} · 已验证记忆 ${j.memory_verified_count||0}/${j.memory_total_count||0} · draft/active ${(100*(j.skill_draft_active_ratio||0)).toFixed(0)}% (${j.skill_draft_count||0}/${j.skill_active_count||0})`)}
      </div>
      ${(j.notes||[]).map(n=>`<div class="hint">${esc(n)}</div>`).join("")}`;
  }catch(e){ el.innerHTML=`<div class="empty-line">效果加载失败：${esc(String(e))}</div>`; }
}
async function loadAIRuns(){
  const el=$("aiRunsList"); if(!el) return;
  try{
    const list=await fetch(`${API}/ai/runs?limit=30`).then(r=>r.json());
    if(!list||!list.length){ el.innerHTML=`<div class="empty-line">暂无 AI Runs（需 PostgreSQL）</div>`; return; }
    el.innerHTML=list.map(r=>{
      const meta=r.meta||{};
      const toolNames=Array.isArray(meta.tools)?meta.tools.filter(Boolean):[];
      const badges=[
        meta.tool_turns?`<span class="badge info">tools×${meta.tool_turns}${toolNames.length?" · "+esc(toolNames.slice(0,4).join(",")):""}</span>`:"",
        meta.fallback_model?`<span class="badge warn">fb:${esc(meta.fallback_model)}</span>`:"",
        meta.live_evidence?`<span class="badge ok">证据${meta.live_evidence}</span>`:"",
        meta.citations?`<span class="badge">cite ${meta.citations}</span>`:"",
      ].join(" ");
      return `<div class="sre-row" data-run="${esc(r.id)}">
      <span class="badge ${r.ok?"ok":"warn"}">${esc(r.kind||"?")}</span>
      <div class="sre-row-main"><div class="sre-row-title">${esc(r.task||r.id)} ${badges}</div>
        <div class="sre-row-sub">${esc(r.actor||"")} · ${esc(r.model||"")} · ${fmtDateTime(r.created_at)} · ${r.latency_ms||0}ms
          ${r.incident_id?` · 事件#${r.incident_id}`:""} ${r.feedback?` · fb=${esc(r.feedback)}`:""}</div>
      </div></div>`;
    }).join("");
    el.querySelectorAll("[data-run]").forEach(row=>row.onclick=async()=>{
      const j=await fetch(`${API}/ai/runs/${encodeURIComponent(row.dataset.run)}`).then(r=>r.json());
      const meta=j.meta?JSON.stringify(j.meta,null,2):"";
      alert(`Run ${j.id}\nkind=${j.kind}\nmodel=${j.model||""}\nok=${j.ok}\nmeta=${meta}\n\n${(j.answer||"").slice(0,1200)}`);
    });
  }catch(e){ el.innerHTML=`<div class="empty-line">Runs 加载失败</div>`; }
}

function openRuleModal(r){
  $("rrId").value=r?r.id:""; $("rrTitle").textContent=r?I18N.t("sre.edit_rule","编辑规则"):I18N.t("sre.new_rule","新建规则");
  $("rrName").value=r?r.name:""; $("rrEnabled").checked=r?r.enabled:true;
  $("rrLevel").value=r?(r.min_level||""):"critical";
  { // 主机分类改为下拉选择：从当前纳管主机的分类去重生成选项（含已保存但当前无主机的分类）
    const cur=r?(r.match_category||""):"";
    const _hs=((typeof LAST_HOSTS!=="undefined"&&LAST_HOSTS)||[]);
    // 包含所有主机分类 + 操作系统类型（去重）
    const cats=[...new Set([..._hs.map(h=>h.category).filter(Boolean), ..._hs.map(h=>h.os).filter(Boolean)])];
    if(cur&&!cats.includes(cur)) cats.push(cur);
    $("rrCategory").innerHTML='<option value="">'+I18N.t("sre.all_categories","全部分类")+'</option>'+cats.map(c=>'<option value="'+esc(c)+'">'+esc(c)+'</option>').join('');
    $("rrCategory").value=cur;
  }
  $("rrCooldown").value=r?r.cooldown_sec:300; $("rrMaxPerHour").value=r?r.max_per_hour:6; $("rrApproval").checked=r?r.require_approval:false;
  if($("rrDryRun")) $("rrDryRun").checked=r?!!r.dry_run:false;
  $("rrPlaybook").innerHTML=SRE_PLAYBOOKS.map(p=>`<option value="${esc(p.id)}" ${r&&r.playbook_id===p.id?"selected":""}>${esc(p.name)}</option>`).join("")||`<option value="">${I18N.t("sre.create_pb_first","（请先创建剧本）")}</option>`;
  if($("rrRollbackPlaybook")){
    $("rrRollbackPlaybook").innerHTML=`<option value="">（无）</option>`+SRE_PLAYBOOKS.map(p=>`<option value="${esc(p.id)}" ${r&&r.rollback_playbook_id===p.id?"selected":""}>${esc(p.name)}</option>`).join("");
  }
  const sel=new Set(r?(r.match_types||[]):[]);
  $("rrTypes").innerHTML=SRE_ALERT_TYPES.map(t=>`<label class="chip-check"><input type="checkbox" value="${esc(t)}" ${sel.has(t)?"checked":""}> ${esc(t)}</label>`).join("");
  $("remediationRuleMask").classList.add("show");
}
async function saveRule(){
  const types=[...document.querySelectorAll("#rrTypes input:checked")].map(c=>c.value);
  const body={id:$("rrId").value,name:$("rrName").value.trim(),enabled:$("rrEnabled").checked,match_types:types,min_level:$("rrLevel").value,match_category:$("rrCategory").value.trim(),playbook_id:$("rrPlaybook").value,require_approval:$("rrApproval").checked,dry_run:$("rrDryRun")?!!$("rrDryRun").checked:false,rollback_playbook_id:($("rrRollbackPlaybook")&&$("rrRollbackPlaybook").value)||"",cooldown_sec:parseInt($("rrCooldown").value)||0,max_per_hour:parseInt($("rrMaxPerHour").value)||0};
  const r=await fetch(`${API}/remediation/rules`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
  const j=await r.json().catch(()=>({}));
  if(r.ok){ $("remediationRuleMask").classList.remove("show"); loadRemediation(); toast(I18N.t("toast.saved","已保存"),"ok"); } else toast(j.error||I18N.t("toast.save_failed","保存失败"),"err");
}

/* ---- SLO ---- */
async function loadSLOs(){
  try {
    SRE_SLOS = (await fetch(`${API}/slos`).then(r=>r.json()))||[];
    // 顺带拉取 apimon 接口清单，供 SLO 表单选择 API 接口作为 SLI 源
    try { const d=await fetch(`${API}/apimon/systems`).then(r=>r.json()); SRE_API_ENDPOINTS=((d&&d.systems)||[]).flatMap(sy=>(sy.endpoints||[]).map(e=>({id:e.id,name:sy.name+" / "+e.name}))); } catch(_){}
    renderSLOs(SRE_SLOS);
  }
  catch(e){ toast(I18N.t("sre.load_failed","加载失败")+": "+e,"err"); }
}
function renderSLOs(list){
  const el=$("sloList");
  if(!list.length){ el.innerHTML=`<div class="empty-line">${I18N.t("sre.no_slo","暂无 SLO")}</div>`; return; }
  el.innerHTML=list.map(s=>{
    const bCls=s.error_budget<=0?"crit":s.error_budget<30?"warn":"ok";
    const src=s.source_type==="check"?I18N.t("sre.slo_check_up_rate","拨测 up 率"):s.source_type==="api"?I18N.t("sre.slo_api_up_rate","API up 率"):s.source_type==="promql"?"PromQL":`${s.metric} ${s.comparator} ${s.threshold}`;
    return `<div class="pb-card fwd-card ${s.enabled?"":"pb-off"}" data-slo="${esc(s.id)}">
      <div class="pb-card-top"><div class="pb-card-title"><strong>${esc(s.name)}</strong><span class="pb-desc">${esc(src)} · ${I18N.t("sre.slo_target","目标")} ${s.target}% · ${s.window_days}d</span></div>
        <span class="badge ${s.breaching?"crit":"ok"}">SLI ${s.sli.toFixed(2)}%</span></div>
      <div class="slo-budget"><div class="slo-budget-bar"><div class="slo-budget-fill ${bCls}" style="width:${Math.max(0,Math.min(100,s.error_budget))}%"></div></div>
        <div class="slo-budget-txt">${I18N.t("sre.slo_error_budget","错误预算")} ${s.error_budget.toFixed(0)}% · ${I18N.t("sre.slo_burn","燃尽")} ${s.burn_rate.toFixed(2)}× · ${I18N.t("sre.slo_good","达标")} ${s.good_events}/${s.total_events}</div></div>
      <div class="pb-card-foot"><div class="pb-pills">${s.breaching?`<span class="badge crit">${I18N.t("sre.slo_breach","超标")}</span>`:`<span class="badge ok">${I18N.t("sre.slo_healthy","健康")}</span>`}${s.burn_state==="fast"?`<span class="badge crit">🔥${I18N.t("sre.slo_burn_fast","快烧")}</span>`:s.burn_state==="slow"?`<span class="badge warn">${I18N.t("sre.slo_burn_slow","慢烧")}</span>`:""}${s.enabled?"":`<span class="badge">${I18N.t("sre.badge_disabled","停用")}</span>`}</div>
        <div class="fwd-actions"><button class="btn sm" data-sloact="trend">${I18N.t("sre.slo_trend","趋势")}</button><button class="btn sm" data-sloact="edit">${I18N.t("ui.edit","编辑")}</button><button class="btn danger sm" data-sloact="del">${I18N.t("ui.delete","删除")}</button></div></div></div>`;
  }).join("");
  el.querySelectorAll("[data-slo]").forEach(card=>card.querySelectorAll("[data-sloact]").forEach(b=>b.onclick=e=>{ e.stopPropagation();
    const id=card.dataset.slo, act=b.dataset.sloact;
    if(act==="trend") openSloTrend(SRE_SLOS.find(x=>x.id===id));
    else if(act==="edit") openSloModal(SRE_SLOS.find(x=>x.id===id));
    else if(act==="del" && confirm(I18N.t("sre.confirm_del_slo","确认删除该 SLO？"))) fetch(`${API}/slos/${id}`,{method:"DELETE"}).then(()=>loadSLOs());
  }));
}
function sloSourceChange(){
  const src=$("sloSource").value;
  $("sloCheckField").style.display=src==="check"?"":"none";
  $("sloApiField").style.display=src==="api"?"":"none";
  $("sloMetricFields").style.display=src==="metric"?"":"none";
  $("sloPromqlFields").style.display=src==="promql"?"":"none";
}
function openSloModal(s){
  $("sloId").value=s?s.id:""; $("sloModalTitle").textContent=s?I18N.t("sre.edit_slo","编辑 SLO"):I18N.t("sre.new_slo","新建 SLO");
  $("sloName").value=s?s.name:""; $("sloEnabled").checked=s?s.enabled:true; $("sloSource").value=s?s.source_type:"check";
  $("sloCheck").innerHTML=SRE_CHECKS.map(c=>`<option value="${esc(c.id)}" ${s&&s.check_id===c.id?"selected":""}>${esc(c.name)}</option>`).join("")||`<option value="">${I18N.t("sre.create_check_first","（请先创建拨测）")}</option>`;
  $("sloApi").innerHTML=SRE_API_ENDPOINTS.map(e=>`<option value="${esc(e.id)}" ${s&&s.api_id===e.id?"selected":""}>${esc(e.name)}</option>`).join("")||`<option value="">${I18N.t("sre.create_api_first","（请先创建 API 监控）")}</option>`;
  $("sloHost").innerHTML=SRE_HOSTS.map(h=>`<option value="${esc(h.id)}" ${s&&s.host_id===h.id?"selected":""}>${esc((window.HostPicker&&HostPicker.optionLabel)?HostPicker.optionLabel(h):(h.hostname||h.id))}</option>`).join("");
  if(s){ $("sloMetric").value=s.metric||"cpu_percent"; $("sloComparator").value=s.comparator||"<"; $("sloThreshold").value=s.threshold||90; } else { $("sloComparator").value="<"; $("sloThreshold").value=90; }
  $("sloTotalQuery").value=s&&s.total_query?s.total_query:""; $("sloGoodQuery").value=s&&s.good_query?s.good_query:"";
  $("sloTarget").value=s?s.target:99.9; $("sloWindow").value=s?s.window_days:30;
  sloSourceChange(); $("sloMask").classList.add("show");
}
async function saveSlo(){
  const src=$("sloSource").value;
  const body={id:$("sloId").value,name:$("sloName").value.trim(),enabled:$("sloEnabled").checked,source_type:src,target:parseFloat($("sloTarget").value)||99,window_days:parseInt($("sloWindow").value)||30};
  if(src==="check") body.check_id=$("sloCheck").value;
  else if(src==="api") body.api_id=$("sloApi").value;
  else if(src==="promql"){ body.total_query=$("sloTotalQuery").value.trim(); body.good_query=$("sloGoodQuery").value.trim(); }
  else { body.host_id=$("sloHost").value; body.metric=$("sloMetric").value; body.comparator=$("sloComparator").value; body.threshold=parseFloat($("sloThreshold").value)||0; }
  const r=await fetch(`${API}/slos`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
  const j=await r.json().catch(()=>({}));
  if(r.ok){ $("sloMask").classList.remove("show"); loadSLOs(); toast(I18N.t("toast.saved","已保存"),"ok"); } else toast(j.error||I18N.t("toast.save_failed","保存失败"),"err");
}

/* ---- SLO 趋势（自定义时间范围，与主机趋势图一致） ---- */
let SLO_TREND = { id:"", name:"", target:99.9, range:24, custom:null };
let SLO_TREND_CHART = null;
function openSloTrend(slo){
  if(!slo) return;
  SLO_TREND = { id:slo.id, name:slo.name, target:slo.target||99.9, range:24, custom:null };
  $("sloTrendTitle").textContent = slo.name + " · SLO 趋势";
  $("sloTrendMask").classList.add("show");
  loadSloTrend();
}
async function loadSloTrend(){
  const { id, name, target, range, custom } = SLO_TREND;
  const body=$("sloTrendBody");
  body.innerHTML=`<div class="empty-line">${I18N.t("ui.loading","加载中…")}</div>`;
  const now=Math.floor(Date.now()/1000);
  const anchorKey="slo-trend:"+id;
  const rangeH=range>0?range:30*24;
  const win=(typeof resolveAnchoredRange==="function")
    ? resolveAnchoredRange(anchorKey, rangeH, custom)
    : { from: custom ? custom.from : (range>0 ? now-range*3600 : now-30*86400), to: custom ? custom.to : now };
  const from=win.from, to=win.to;
  const ctrl = `${renderChartControls(custom?-1:range,"slorange")}
    <button class="chip-btn ${custom?"active":""}" data-slo-custom-toggle title="${I18N.t("time.custom_range","自定义时间范围")}">${I18N.t("time.custom","自定义")}</button>
    ${typeof forecastChipHTML==="function"?forecastChipHTML("slo-trend"):""}
    <span class="chart-custom-range" id="sloCustomPanel"${custom?"":" hidden"}>
      <input type="datetime-local" id="sloCustomFrom" class="dt-input" value="${toLocalDatetimeValue(from>0?from:now-86400)}">
      <span class="dt-sep">→</span>
      <input type="datetime-local" id="sloCustomTo" class="dt-input" value="${toLocalDatetimeValue(to)}">
      <button class="chip-btn primary" data-slo-custom-apply>${I18N.t("time.custom_apply","应用")}</button>
    </span>`;
  const load=(typeof beginRangeLoad==="function")
    ? beginRangeLoad(anchorKey)
    : { signal:undefined, isCurrent:()=>true };
  try{
    const fetchOpts=load.signal?{signal:load.signal}:undefined;
    const d=await fetch(`${API}/slos/${encodeURIComponent(id)}/trend?from=${from}&to=${to}`, fetchOpts).then(r=>r.json());
    if(!load.isCurrent()) return;
    const trend=(d&&d.trend)||[], st=(d&&d.status)||{};
    if(!trend.length){
      body.innerHTML=`<div class="chart-controls">${ctrl}</div><div class="empty-line">该时间范围暂无数据（SLO 数据源运行 / 积累后出现）。</div>`;
      return;
    }
    const samples=trend.map(p=>({timestamp:p.timestamp, sli:p.sli}));
    const bCls=(st.error_budget||0)<=0?"crit":((st.error_budget||0)<30?"warn":"ok");
    body.innerHTML=`<div class="chart-controls">${ctrl}</div>
      <div class="api-hist-stat">
        <span class="ahs"><b class="${st.breaching?"crit":"ok"}">${(st.sli||0).toFixed(3)}%</b><i>区间 SLI（目标 ${target}%）</i></span>
        <span class="ahs"><b class="${bCls}">${(st.error_budget||0).toFixed(0)}%</b><i>剩余错误预算</i></span>
        <span class="ahs"><b>${(st.burn_rate||0).toFixed(2)}×</b><i>燃尽速率</i></span>
        <span class="ahs"><b>${st.good_events||0}/${st.total_events||0}</b><i>达标 / 总</i></span>
      </div>
      <div class="chart-container"><div class="chart-wrap"><div class="chart-sub-title">SLI 趋势（每桶可用率 %）</div><canvas id="sloTrendCanvas" width="1000" height="240"></canvas></div></div>
      <div class="hint">按所选时间范围分桶现算每段可用率；可切换快捷跨度或自定义绝对区间（与主机趋势图一致）。y 轴自适应放大以显现波动。</div>`;
    const ser=[{ key:"sli", label:I18N.t("sre.slo_sli","SLI"), color:"#4c8dff", fmt:v=>v.toFixed(3)+"%" }];
    if(!load.isCurrent()) return;
    if(typeof createChartWithForecast==="function" && isChartForecastOn("slo-trend")){
      SLO_TREND_CHART = await createChartWithForecast("sloTrendCanvas", samples, ser, null, 100, {
        title:"", legendMode:"dash", cssH:220, forecast:true, forecastScope:"slo-trend",
        signal:load.signal, isCurrent:load.isCurrent
      });
    } else {
      SLO_TREND_CHART = createChart("sloTrendCanvas", samples, ser, null, 100, { title: "", legendMode: "dash", cssH: 220 });
    }
  }catch(e){
    if(e && (e.name==="AbortError" || /aborted/i.test(String(e.message||e)))) return;
    if(!load.isCurrent()) return;
    body.innerHTML=`<div class="empty-line">加载失败：${esc(e)}</div>`;
  }
}
function applySloCustomRange(){
  applyCustomRangeFromInputs($("sloCustomFrom"), $("sloCustomTo"), (from, to) => {
    SLO_TREND.custom={from,to}; loadSloTrend();
  });
}
safeAddEventListener("sloTrendBody","click",e=>{
  const tog=e.target.closest("[data-slo-custom-toggle]");
  if(tog){ const p=$("sloCustomPanel"); if(p) p.hidden=!p.hidden; return; }
  if(e.target.closest("[data-slo-custom-apply]")){ applySloCustomRange(); return; }
  const rb=e.target.closest(".chip-btn[data-slorange]");
  if(rb){
    const next=parseInt(rb.dataset.slorange);
    if(SLO_TREND.custom||SLO_TREND.range!==next){
      if(typeof clearAnchoredRange==="function"&&SLO_TREND.id) clearAnchoredRange("slo-trend:"+SLO_TREND.id);
    }
    SLO_TREND.custom=null; SLO_TREND.range=next; loadSloTrend(); return;
  }
});

/* ---- 工单 ---- */
let TK_KIND_FILTER="";
let SR_CATALOG=[];
async function loadTickets(){
  try {
    const q=TK_KIND_FILTER?`?kind=${encodeURIComponent(TK_KIND_FILTER)}`:"";
    SRE_TICKETS=(await fetch(`${API}/tickets${q}`).then(r=>r.json()))||[];
    renderTickets(SRE_TICKETS);
  } catch(e){ toast(I18N.t("sre.load_failed","加载失败")+": "+e,"err"); }
}
function renderTickets(list){
  const el=$("ticketList");
  if(!list.length){ el.innerHTML=`<div class="empty-line">${I18N.t("sre.no_tickets","暂无工单")}</div>`; return; }
  const kindLabel=k=>({incident:"事件",service_request:"服务请求",task:"任务"})[k]||k||"task";
  el.innerHTML=list.map(t=>`<div class="sre-row" data-ticket="${t.id}">
    <span class="badge ${_prioCls(t.priority)}">${esc((t.priority||"p3").toUpperCase())}</span>
    <div class="sre-row-main"><div class="sre-row-title">${esc(t.title)}</div>
      <div class="sre-row-sub">#${t.id} · ${esc(kindLabel(t.kind))}${t.catalog_item?" · "+esc(t.catalog_item):""}${t.assignee?" · @"+esc(t.assignee):""}${t.incident_id?" · 🔗"+I18N.t("sre.event","事件")+"#"+t.incident_id:""}${(t.links&&t.links.length)?" · 🔗×"+t.links.length:""} · ${fmtDateTime(t.updated_at)}</div></div>
    <span class="badge ${_tkStatusCls(t.status)}">${esc(t.status)}</span></div>`).join("");
  el.querySelectorAll("[data-ticket]").forEach(row=>row.onclick=()=>openTicketModal(SRE_TICKETS.find(x=>x.id==row.dataset.ticket)));
}
function renderTkLinks(links){
  const el=$("tkLinksChips");
  if(!el) return;
  const list=links||[];
  if(!list.length){ el.innerHTML=""; el.style.display="none"; return; }
  el.style.display="";
  el.innerHTML=list.map(l=>`<span class="tag" title="${esc(l.role||"")}">${esc(l.type)}:${esc(l.id)}${l.name?" "+esc(l.name):""}</span>`).join(" ");
}
async function ensureSRCatalog(){
  if(SR_CATALOG.length) return SR_CATALOG;
  try{ SR_CATALOG=(await fetch(`${API}/service-request/catalog`).then(r=>r.json()))||[]; }catch(e){ SR_CATALOG=[]; }
  return SR_CATALOG;
}
function fillTkCatalog(selected){
  const sel=$("tkCatalog"); if(!sel) return;
  sel.innerHTML=`<option value="">— 选择目录项 —</option>`+(SR_CATALOG||[]).map(it=>
    `<option value="${esc(it.id)}"${selected===it.id?" selected":""}>${esc(it.category||"")} · ${esc(it.title||it.id)}</option>`).join("");
}
function syncTkKindUI(){
  const kind=($("tkKind")&&$("tkKind").value)||"task";
  const cf=$("tkCatalogField");
  if(cf) cf.style.display = kind==="service_request" ? "" : "none";
}
let TK_CREATE_ATTACHMENTS=[];
let TK_COMMENT_ATTACHMENTS=[];
function refreshTkCreateAtt(){ renderAttachBox($("tkCreateAttach"), TK_CREATE_ATTACHMENTS, i=>{ TK_CREATE_ATTACHMENTS.splice(i,1); refreshTkCreateAtt(); }); }
function refreshTkCommentAtt(){ renderAttachBox($("tkCommentAttach"), TK_COMMENT_ATTACHMENTS, i=>{ TK_COMMENT_ATTACHMENTS.splice(i,1); refreshTkCommentAtt(); }); }

async function openTicketModal(t){
  await ensureSRCatalog();
  fillTkCatalog(t&&t.catalog_item||"");
  $("ticketId").value=t?t.id:""; $("ticketModalTitle").textContent=t?`#${t.id} ${t.title}`:I18N.t("sre.new_ticket","新建工单");
  if($("tkKind")) $("tkKind").value=t?(t.kind||(t.incident_id?"incident":"task")):"task";
  syncTkKindUI();
  $("tkTitle").value=t?t.title:""; $("tkPriority").value=t?t.priority:"p3"; $("tkStatus").value=t?t.status:"open";
  $("tkDesc").value=t?(t.description||""):"";
  await fillUserSelect($("tkAssignee"), t?(t.assignee||""):"");
  TK_CREATE_ATTACHMENTS=[]; TK_COMMENT_ATTACHMENTS=[]; refreshTkCreateAtt(); refreshTkCommentAtt();
  renderTkLinks(t&&t.links||[]);
  ["tkLinkHost","tkLinkSLO","tkLinkChange","tkLinkSQL"].forEach(id=>{ const el=$(id); if(el) el.value=""; });
  const attachField=$("tkAttachField");
  if (attachField) attachField.style.display = t ? "none" : "";
  // Show linked incident info if present
  const incInfo=$("tkIncidentInfo");
  if(t && t.incident){
    const inc=t.incident;
    incInfo.innerHTML=`<div class="hint" style="margin-bottom:8px">🔗 ${I18N.t("sre.linked_incident","关联事件")}：<a href="#" onclick="openIncidentDetail(${inc.id});return false" style="font-weight:600">#${inc.id} ${esc(inc.title)}</a> · <span class="badge ${_sevCls(inc.severity)}">${esc(inc.severity)}</span> · ${esc(inc.hostname||"")} · ${fmtDateTime(inc.created_at)}</div>`;
    incInfo.style.display="";
  } else if(t && t.incident_id){
    incInfo.innerHTML=`<div class="hint" style="margin-bottom:8px">🔗 ${I18N.t("sre.linked_incident","关联事件")}：<a href="#" onclick="openIncidentDetail(${t.incident_id});return false" style="font-weight:600">#${t.incident_id}</a></div>`;
    incInfo.style.display="";
  } else { incInfo.style.display="none"; }
  const cm=$("tkComments"),cf=$("tkCommentField");
  if(t){
    const createAtts = (t.attachments&&t.attachments.length)?`<div class="hint" style="margin-bottom:8px">创建附件</div>${attachChipsHTML(t.attachments)}`:"";
    cm.innerHTML=`${createAtts}<div class="subhead">${I18N.t("sre.comments","评论")}</div>`+((t.comments||[]).map(c=>`<div class="tk-comment"><span class="tk-c-author">${esc(c.author)}</span> <span class="tk-c-time">${fmtDateTime(c.ts)}</span><div>${esc(c.text)}</div>${attachChipsHTML(c.attachments)}</div>`).join("")||`<div class="empty-line">—</div>`);
    cf.style.display="";
  } else { cm.innerHTML=""; cf.style.display="none"; }
  $("ticketMask").classList.add("show");
}
async function saveTicket(){
  const id=$("ticketId").value;
  const body={
    title:$("tkTitle").value.trim(),priority:$("tkPriority").value,status:$("tkStatus").value,
    assignee:($("tkAssignee").value||"").trim(),description:$("tkDesc").value.trim(),
    kind:($("tkKind")&&$("tkKind").value)||"task",
    catalog_item:($("tkCatalog")&&$("tkCatalog").value)||""
  };
  if(!id && TK_CREATE_ATTACHMENTS.length) body.attachments = attachmentsToAPI(TK_CREATE_ATTACHMENTS);
  if(!body.title){ toast(I18N.t("sre.fill_title","请填写标题"),"err"); return; }
  const r=await fetch(id?`${API}/tickets/${id}`:`${API}/tickets`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
  const j=await r.json().catch(()=>({}));
  if(r.ok){
    const tid=j.id||id;
    // Apply pending link fields after create/update
    const add=[];
    const h=($("tkLinkHost")&&$("tkLinkHost").value||"").trim(); if(h) add.push({type:"host",id:h,role:"affects"});
    const slo=($("tkLinkSLO")&&$("tkLinkSLO").value||"").trim(); if(slo) add.push({type:"slo",id:slo,role:"caused_by"});
    const chg=($("tkLinkChange")&&$("tkLinkChange").value||"").trim(); if(chg) add.push({type:"change",id:chg,role:"related"});
    const sql=($("tkLinkSQL")&&$("tkLinkSQL").value||"").trim(); if(sql) add.push({type:"sql_change",id:sql,role:"implements"});
    if(tid && add.length){
      await fetch(`${API}/tickets/${tid}/link`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({add})});
    }
    $("ticketMask").classList.remove("show"); TK_CREATE_ATTACHMENTS=[]; loadTickets(); loadSREBadge(); loadIncidents();
    const closed = body.status==="resolved" || body.status==="closed";
    const linked = (j&&j.incident_id) || (window._curIncident&&window._curIncident.ticket_id==id&&window._curIncident.id);
    if(id && closed && linked) toast(I18N.t("sre.ticket_closed_inc","工单已关闭，关联事件已标记解决"),"ok");
    else toast(I18N.t("toast.saved","已保存"),"ok");
    if(window._curIncident && Number(window._curIncident.id)===Number(j.incident_id||window._curIncident.id)){
      openIncidentDetail(window._curIncident.id);
    }
  } else toast(j.error||I18N.t("toast.save_failed","保存失败"),"err");
}
async function addTicketLinksNow(){
  const id=$("ticketId").value;
  if(!id){ toast("请先保存工单后再添加关联，或填写后一并保存","err"); return; }
  const add=[];
  const h=($("tkLinkHost")&&$("tkLinkHost").value||"").trim(); if(h) add.push({type:"host",id:h,role:"affects"});
  const slo=($("tkLinkSLO")&&$("tkLinkSLO").value||"").trim(); if(slo) add.push({type:"slo",id:slo,role:"caused_by"});
  const chg=($("tkLinkChange")&&$("tkLinkChange").value||"").trim(); if(chg) add.push({type:"change",id:chg,role:"related"});
  const sql=($("tkLinkSQL")&&$("tkLinkSQL").value||"").trim(); if(sql) add.push({type:"sql_change",id:sql,role:"implements"});
  if(!add.length){ toast("请填写至少一个关联 ID","err"); return; }
  const r=await fetch(`${API}/tickets/${id}/link`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({add})});
  const j=await r.json().catch(()=>({}));
  if(!r.ok){ toast(j.error||"关联失败","err"); return; }
  toast("关联已更新","ok"); openTicketModal(j); loadTickets();
}
async function addTicketComment(){
  const id=$("ticketId").value,t=$("tkCommentInput").value.trim();
  const atts=TK_COMMENT_ATTACHMENTS.slice();
  if(!id||(!t && !atts.length))return;
  await fetch(`${API}/tickets/${id}/comment`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({text:t,attachments:attachmentsToAPI(atts)})});
  $("tkCommentInput").value=""; TK_COMMENT_ATTACHMENTS=[]; refreshTkCommentAtt();
  const tk=await fetch(`${API}/tickets/${id}`).then(r=>r.json()); openTicketModal(tk); loadTickets();
}

document.querySelectorAll("#sreTabs .chip-btn").forEach(b=>b.addEventListener("click",()=>switchSRETab(b.dataset.sretab)));
safeAddEventListener("newIncidentBtn","click",openNewIncident);
safeAddEventListener("niSaveBtn","click",saveNewIncident);
safeAddEventListener("newRemediationBtn","click",()=>openRuleModal(null));
safeAddEventListener("rrSaveBtn","click",saveRule);
safeAddEventListener("newSloBtn","click",()=>openSloModal(null));
safeAddEventListener("sloSaveBtn","click",saveSlo);
safeAddEventListener("sloSource","change",sloSourceChange);
safeAddEventListener("newTicketBtn","click",()=>openTicketModal(null));
safeAddEventListener("tkSaveBtn","click",saveTicket);
safeAddEventListener("tkCommentBtn","click",addTicketComment);
safeAddEventListener("tkKind","change",syncTkKindUI);
safeAddEventListener("tkCatalog","change",()=>{
  const id=($("tkCatalog")&&$("tkCatalog").value)||"";
  const it=(SR_CATALOG||[]).find(x=>x.id===id);
  if(!it) return;
  if($("tkKind")) $("tkKind").value="service_request";
  syncTkKindUI();
  if($("tkTitle") && !$("tkTitle").value.trim()) $("tkTitle").value=it.title||"";
  if($("tkDesc") && !$("tkDesc").value.trim()) $("tkDesc").value=it.description||"";
  if($("tkPriority") && it.priority) $("tkPriority").value=it.priority;
});
safeAddEventListener("tkLinkAddBtn","click",addTicketLinksNow);
document.querySelectorAll("#ticketKindFilter .chip-btn").forEach(b=>b.addEventListener("click",()=>{
  document.querySelectorAll("#ticketKindFilter .chip-btn").forEach(x=>x.classList.remove("active"));
  b.classList.add("active");
  TK_KIND_FILTER=b.dataset.tkind||"";
  loadTickets();
}));
safeAddEventListener("tkAttachBtn","click",()=>{ const f=$("tkAttachFile"); if(f) f.click(); });
safeAddEventListener("tkCommentAttachBtn","click",()=>{ const f=$("tkCommentFile"); if(f) f.click(); });
const _tkAf=$("tkAttachFile"); if(_tkAf) _tkAf.onchange=async()=>{ await ingestFilesIntoAttachments(_tkAf.files, TK_CREATE_ATTACHMENTS, {onChange:refreshTkCreateAtt}); refreshTkCreateAtt(); _tkAf.value=""; };
const _tkCf=$("tkCommentFile"); if(_tkCf) _tkCf.onchange=async()=>{ await ingestFilesIntoAttachments(_tkCf.files, TK_COMMENT_ATTACHMENTS, {onChange:refreshTkCommentAtt}); refreshTkCommentAtt(); _tkCf.value=""; };
safeAddEventListener("ocRefreshWhoBtn","click",loadOnCall);
safeAddEventListener("newOnCallSchedBtn","click",()=>openOnCallSchedModal(null));
safeAddEventListener("newEscPolicyBtn","click",()=>openEscPolicyModal(null));
safeAddEventListener("newChangeWinBtn","click",()=>openChangeWinModal(null));
safeAddEventListener("newChangeRecBtn","click",()=>openChangeRecModal(null));

/* ---- On-call ---- */
async function loadOnCall(){
  try{
    const [who, schs, pols, pages]=await Promise.all([
      fetch(`${API}/oncall/who`).then(r=>r.json()),
      fetch(`${API}/oncall/schedules`).then(r=>r.json()),
      fetch(`${API}/oncall/policies`).then(r=>r.json()),
      fetch(`${API}/oncall/pages?open=1`).then(r=>r.json())
    ]);
    const wh=$("oncallWhoList");
    if(!who||!who.length) wh.innerHTML=`<div class="empty-line">暂无排班</div>`;
    else wh.innerHTML=who.map(w=>`<div class="sre-row"><div class="sre-row-main"><div class="sre-row-title">${esc(w.name||w.id)}</div>
      <div class="sre-row-sub">主值班：<b>${esc(w.primary||"—")}</b>${(w.layers||[]).map(l=>` · ${esc(l.name||"")}=${esc(l.current||"—")}`).join("")}</div></div></div>`).join("");
    const sl=$("oncallSchedList");
    if(!schs||!schs.length) sl.innerHTML=`<div class="empty-line">暂无排班表</div>`;
    else sl.innerHTML=schs.map(s=>{
      const mem=((s.layers||[])[0]||{}).members||[];
      return `<div class="fwd-card"><div class="fwd-card-title">${esc(s.name||s.id)}</div>
        <div class="fwd-card-sub mono">${esc(s.timezone||"Asia/Shanghai")} · ${mem.length} 人</div>
        <div class="fwd-card-acts"><button class="btn sm" data-oc="edit-sched" data-id="${esc(s.id)}">编辑</button>
        <button class="btn danger sm" data-oc="del-sched" data-id="${esc(s.id)}">删除</button></div></div>`;
    }).join("");
    sl.querySelectorAll("[data-oc]").forEach(b=>b.onclick=()=>oncallAct(b.dataset.oc,b.dataset.id));
    const pl=$("oncallPolicyList");
    if(!pols||!pols.length) pl.innerHTML=`<div class="empty-line">暂无升级策略</div>`;
    else pl.innerHTML=pols.map(p=>`<div class="fwd-card"><div class="fwd-card-title">${esc(p.name||p.id)} ${p.enabled?"":"(停用)"}</div>
      <div class="fwd-card-sub">${(p.steps||[]).length} 级升级</div>
      <div class="fwd-card-acts"><button class="btn sm" data-oc="edit-pol" data-id="${esc(p.id)}">编辑</button>
      <button class="btn danger sm" data-oc="del-pol" data-id="${esc(p.id)}">删除</button></div></div>`).join("");
    pl.querySelectorAll("[data-oc]").forEach(b=>b.onclick=()=>oncallAct(b.dataset.oc,b.dataset.id));
    const pg=$("oncallPageList");
    if(!pages||!pages.length) pg.innerHTML=`<div class="empty-line">无进行中的 Page</div>`;
    else pg.innerHTML=pages.map(p=>`<div class="sre-row"><div class="sre-row-main"><div class="sre-row-title">Page #${p.id} · 事件 #${p.incident_id}</div>
      <div class="sre-row-sub">${esc(p.status)} · step ${p.step}${(p.notified||[]).length?" · "+esc((p.notified||[]).join(",")):""}${p.next_escalate_at?" · 下次升级 "+fmtDateTime(p.next_escalate_at):""}</div></div></div>`).join("");
  }catch(e){ toast("加载 On-call 失败: "+e,"err"); }
}
async function oncallAct(act,id){
  if(act==="del-sched"){
    if(!confirm("删除排班表？")) return;
    await fetch(`${API}/oncall/schedules/${encodeURIComponent(id)}`,{method:"DELETE"});
    loadOnCall(); return;
  }
  if(act==="del-pol"){
    if(!confirm("删除升级策略？")) return;
    await fetch(`${API}/oncall/policies/${encodeURIComponent(id)}`,{method:"DELETE"});
    loadOnCall(); return;
  }
  if(act==="edit-sched"){
    const list=await fetch(`${API}/oncall/schedules`).then(r=>r.json());
    openOnCallSchedModal((list||[]).find(x=>x.id===id)||null); return;
  }
  if(act==="edit-pol"){
    const list=await fetch(`${API}/oncall/policies`).then(r=>r.json());
    openEscPolicyModal((list||[]).find(x=>x.id===id)||null); return;
  }
}
function openOnCallSchedModal(sch){
  $("oncallEditTitle").textContent=sch?"编辑排班":"新建排班";
  const layer=(sch&&sch.layers&&sch.layers[0])||{name:"primary",rotation:"weekly",handoff_at:"10:00",members:[]};
  $("oncallEditBody").innerHTML=`
    <div class="field"><label>名称</label><input id="ocName" value="${esc(sch&&sch.name||"")}"></div>
    <div class="field"><label>时区</label><input id="ocTz" value="${esc(sch&&sch.timezone||"Asia/Shanghai")}"></div>
    <div class="field"><label>轮值</label><div class="select-wrap"><select id="ocRot"><option value="weekly"${layer.rotation==="weekly"?" selected":""}>weekly</option><option value="daily"${layer.rotation==="daily"?" selected":""}>daily</option></select></div></div>
    <div class="field"><label>交接时刻 HH:MM</label><input id="ocHandoff" value="${esc(layer.handoff_at||"10:00")}"></div>
    <div class="field"><label>成员用户名（逗号分隔）</label><input id="ocMembers" value="${esc((layer.members||[]).join(","))}"></div>
    <input type="hidden" id="ocId" value="${esc(sch&&sch.id||"")}">`;
  $("oncallEditMask").classList.add("show");
  $("oncallEditSave").onclick=async()=>{
    const members=($("ocMembers").value||"").split(",").map(s=>s.trim()).filter(Boolean);
    const body={id:$("ocId").value||undefined,name:$("ocName").value.trim(),timezone:$("ocTz").value.trim()||"Asia/Shanghai",
      layers:[{name:"primary",rotation:$("ocRot").value,handoff_at:$("ocHandoff").value.trim()||"10:00",members}]};
    const r=await fetch(`${API}/oncall/schedules`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    const j=await r.json().catch(()=>({}));
    if(r.ok){ $("oncallEditMask").classList.remove("show"); loadOnCall(); toast("已保存","ok"); }
    else toast(j.error||"保存失败","err");
  };
}
function openEscPolicyModal(pol){
  $("oncallEditTitle").textContent=pol?"编辑升级策略":"新建升级策略";
  const steps=pol&&pol.steps&&pol.steps.length?pol.steps:[{after_sec:0,target:{users:[]},channels:["feishu"]},{after_sec:900,target:{users:[]},channels:["feishu","sms"]}];
  $("oncallEditBody").innerHTML=`
    <div class="field"><label>名称</label><input id="epName" value="${esc(pol&&pol.name||"")}"></div>
    <label class="switch mb"><input type="checkbox" id="epEnabled" ${!pol||pol.enabled?"checked":""}> 启用</label>
    <div class="field"><label>排班 ID（可选，绑定 schedule）</label><input id="epSched" value="${esc((steps[0]&&steps[0].target&&steps[0].target.schedule_id)||"")}" placeholder="留空则用成员列表"></div>
    <div class="field"><label>第 1 级成员（逗号）</label><input id="epU0" value="${esc((((steps[0]||{}).target||{}).users||[]).join(","))}"></div>
    <div class="field"><label>第 1 级渠道</label><input id="epC0" value="${esc(((steps[0]||{}).channels||["feishu"]).join(","))}"></div>
    <div class="field"><label>升级等待秒数（到第 2 级）</label><input type="number" id="epAfter1" value="${(steps[1]&&steps[1].after_sec)||900}"></div>
    <div class="field"><label>第 2 级成员（逗号）</label><input id="epU1" value="${esc((((steps[1]||{}).target||{}).users||[]).join(","))}"></div>
    <div class="field"><label>第 2 级渠道</label><input id="epC1" value="${esc(((steps[1]||{}).channels||["feishu"]).join(","))}"></div>
    <input type="hidden" id="epId" value="${esc(pol&&pol.id||"")}">`;
  $("oncallEditMask").classList.add("show");
  $("oncallEditSave").onclick=async()=>{
    const sid=($("epSched").value||"").trim();
    const mk=(users,channels,after,layer)=>({after_sec:after|0,target:{schedule_id:sid||undefined,layer:layer|0,users:users},channels});
    const u0=($("epU0").value||"").split(",").map(s=>s.trim()).filter(Boolean);
    const u1=($("epU1").value||"").split(",").map(s=>s.trim()).filter(Boolean);
    const c0=($("epC0").value||"").split(",").map(s=>s.trim()).filter(Boolean);
    const c1=($("epC1").value||"").split(",").map(s=>s.trim()).filter(Boolean);
    const body={id:$("epId").value||undefined,name:$("epName").value.trim(),enabled:$("epEnabled").checked,
      steps:[mk(u0,c0,0,0),mk(u1,c1,parseInt($("epAfter1").value,10)||900,0)]};
    const r=await fetch(`${API}/oncall/policies`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    const j=await r.json().catch(()=>({}));
    if(r.ok){ $("oncallEditMask").classList.remove("show"); loadOnCall(); toast("已保存","ok"); }
    else toast(j.error||"保存失败","err");
  };
}

/* ---- 变更窗 / 变更记录 ---- */
async function loadChanges(){
  try{
    const [wins, recs]=await Promise.all([
      fetch(`${API}/changes/windows`).then(r=>r.json()),
      fetch(`${API}/changes`).then(r=>r.json())
    ]);
    const wl=$("changeWinList");
    if(!wins||!wins.length) wl.innerHTML=`<div class="empty-line">暂无变更窗</div>`;
    else {
      const now=Math.floor(Date.now()/1000);
      wl.innerHTML=wins.map(w=>{
        const recur=String(w.recur||"").trim();
        let active=false;
        if(w.freeze){
          if(recur){
            const d=new Date(); const cur=d.getHours()*60+d.getMinutes();
            const parseHM=s=>{ const p=String(s||"").split(":"); if(p.length!==2) return -1; const h=+p[0],m=+p[1]; return (h>=0&&h<=23&&m>=0&&m<=59)?h*60+m:-1; };
            const a=parseHM(w.recur_start_hm), b=parseHM(w.recur_end_hm);
            if(a>=0&&b>=0) active=b>a?(cur>=a&&cur<b):(cur>=a||cur<b);
            if(active&&recur==="weekly"&&(w.recur_weekdays||[]).length){
              const wd=d.getDay();
              active=(w.recur_weekdays||[]).map(Number).includes(wd);
            }
          } else {
            active=now>=(w.start||0) && (!w.end || now<=w.end);
          }
        }
        const sched=recur
          ? `循环 ${esc(recur)} ${esc(w.recur_start_hm||"")}–${esc(w.recur_end_hm||"")}${(w.recur_weekdays||[]).length?" · 周"+(w.recur_weekdays||[]).join(","):""}`
          : `${fmtDateTime(w.start)} → ${fmtDateTime(w.end)}`;
        return `<div class="fwd-card"><div class="fwd-card-title">${esc(w.name||w.id)} ${w.freeze?'<span class="badge warn">freeze</span>':""}${recur?` <span class="badge info">${esc(recur)}</span>`:""}${active&&w.freeze?` <span class="badge freeze">${I18N.t("sre.freeze_active","冻结中")}</span>`:""}</div>
      <div class="fwd-card-sub">${sched}${(w.host_ids||[]).length?" · hosts "+(w.host_ids||[]).length:""}${(w.categories||[]).length?" · cat "+esc((w.categories||[]).join(",")):""}</div>
      <div class="fwd-card-acts"><button class="btn sm" data-ch="edit-win" data-id="${esc(w.id)}">编辑</button>
      <button class="btn danger sm" data-ch="del-win" data-id="${esc(w.id)}">删除</button></div></div>`;
      }).join("");
    }
    wl.querySelectorAll("[data-ch]").forEach(b=>b.onclick=()=>changeAct(b.dataset.ch,b.dataset.id));
    const rl=$("changeRecList");
    if(!recs||!recs.length) rl.innerHTML=`<div class="empty-line">暂无变更记录</div>`;
    else rl.innerHTML=recs.map(c=>`<div class="sre-row" data-ch="edit-rec" data-id="${c.id}"><div class="sre-row-main"><div class="sre-row-title">#${c.id} ${esc(c.title)}</div>
      <div class="sre-row-sub">${esc(c.kind)} · <span class="badge">${esc(c.status)}</span> · ${esc(c.risk)} · ${fmtDateTime(c.started_at)}${(c.sql_change_ids||[]).length?" · SQL×"+(c.sql_change_ids||[]).length:""}${(c.host_ids||[]).length?" · "+esc((c.host_ids||[]).slice(0,3).join(",")):""}</div></div></div>`).join("");
    rl.querySelectorAll("[data-ch]").forEach(b=>b.onclick=()=>changeAct(b.dataset.ch,b.dataset.id));
  }catch(e){ toast("加载变更失败: "+e,"err"); }
}
async function changeAct(act,id){
  if(act==="del-win"){
    if(!confirm("删除变更窗？")) return;
    await fetch(`${API}/changes/windows/${encodeURIComponent(id)}`,{method:"DELETE"});
    loadChanges(); return;
  }
  if(act==="edit-win"){
    const list=await fetch(`${API}/changes/windows`).then(r=>r.json());
    openChangeWinModal((list||[]).find(x=>x.id===id)||null); return;
  }
  if(act==="edit-rec"){
    const list=await fetch(`${API}/changes`).then(r=>r.json());
    openChangeRecModal((list||[]).find(x=>String(x.id)===String(id))||null); return;
  }
}
function _dtLocal(ts){
  if(!ts) return "";
  const d=new Date(ts*1000);
  const p=n=>String(n).padStart(2,"0");
  return `${d.getFullYear()}-${p(d.getMonth()+1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}
function openChangeWinModal(w){
  $("changeEditTitle").textContent=w?"编辑变更窗":"新建变更窗";
  const now=Math.floor(Date.now()/1000);
  const recur=w&&w.recur||"";
  $("changeEditBody").innerHTML=`
    <div class="field"><label>名称</label><input id="cwName" value="${esc(w&&w.name||"")}"></div>
    <div class="field"><label>循环模式</label><div class="select-wrap"><select id="cwRecur">
      <option value=""${!recur?" selected":""}>绝对时间窗</option>
      <option value="daily"${recur==="daily"?" selected":""}>每日循环</option>
      <option value="weekly"${recur==="weekly"?" selected":""}>每周循环</option>
    </select></div></div>
    <div class="grid2" id="cwAbsRow">
      <div class="field"><label>开始</label><input type="datetime-local" id="cwStart" value="${_dtLocal(w&&w.start||now)}"></div>
      <div class="field"><label>结束</label><input type="datetime-local" id="cwEnd" value="${_dtLocal(w&&w.end||now+3600)}"></div>
    </div>
    <div class="grid2" id="cwRecurRow" style="display:none">
      <div class="field"><label>每日开始 HH:MM</label><input id="cwRecurStart" value="${esc(w&&w.recur_start_hm||"22:00")}" placeholder="22:00"></div>
      <div class="field"><label>每日结束 HH:MM</label><input id="cwRecurEnd" value="${esc(w&&w.recur_end_hm||"06:00")}" placeholder="06:00"></div>
    </div>
    <div class="field" id="cwWeekRow" style="display:none"><label>星期（0=周日…6=周六，逗号，空=每天）</label>
      <input id="cwWeekdays" value="${esc((w&&w.recur_weekdays||[]).join(","))}" placeholder="1,2,3,4,5"></div>
    <div class="field"><label>作用主机（空=全局）</label>
      <input type="hidden" id="cwHosts" value="${esc((w&&w.host_ids||[]).join(","))}">
      <div id="cwHostPick"></div>
    </div>
    <div class="field"><label>分类（逗号）</label><input id="cwCats" value="${esc((w&&w.categories||[]).join(","))}"></div>
    <label class="switch mb"><input type="checkbox" id="cwFreeze" ${!w||w.freeze?"checked":""}> 冻结期（禁止未审批自愈 / 触发远程闸门）</label>
    <div class="field"><label>备注</label><input id="cwNote" value="${esc(w&&w.note||"")}"></div>
    <input type="hidden" id="cwId" value="${esc(w&&w.id||"")}">`;
  const syncRecurUI=()=>{
    const m=$("cwRecur").value;
    $("cwAbsRow").style.display=m? "none":"";
    $("cwRecurRow").style.display=m? "":"none";
    $("cwWeekRow").style.display=m==="weekly"? "":"none";
  };
  $("cwRecur").onchange=syncRecurUI; syncRecurUI();
  $("changeEditMask").classList.add("show");
  (async () => {
    if (typeof loadHostFolders === "function") { try { await loadHostFolders(); } catch (_) {} }
    if ((!LAST_HOSTS || !LAST_HOSTS.length) && typeof fetchHostsList === "function") {
      try { await fetchHostsList({ force: false }); } catch (_) {}
    }
    sreMountHostMultiPick("cwHostPick", "cwHosts", (w && w.host_ids) || []);
  })();
  $("changeEditSave").onclick=async()=>{
    const toUnix=v=>{ const t=Date.parse(v); return isNaN(t)?0:Math.floor(t/1000); };
    const recurMode=$("cwRecur").value;
    const body={id:$("cwId").value||undefined,name:$("cwName").value.trim(),
      host_ids:($("cwHosts").value||"").split(",").map(s=>s.trim()).filter(Boolean),
      categories:($("cwCats").value||"").split(",").map(s=>s.trim()).filter(Boolean),
      freeze:$("cwFreeze").checked,note:$("cwNote").value.trim(),
      recur:recurMode||undefined,
      recur_start_hm:recurMode?($("cwRecurStart").value||"").trim():undefined,
      recur_end_hm:recurMode?($("cwRecurEnd").value||"").trim():undefined,
      recur_weekdays:recurMode==="weekly"?($("cwWeekdays").value||"").split(",").map(s=>parseInt(s.trim(),10)).filter(n=>n>=0&&n<=6):undefined,
      start:recurMode?0:toUnix($("cwStart").value),
      end:recurMode?0:toUnix($("cwEnd").value)};
    const r=await fetch(`${API}/changes/windows`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    const j=await r.json().catch(()=>({}));
    if(r.ok){ $("changeEditMask").classList.remove("show"); loadChanges(); toast("已保存","ok"); }
    else toast(j.error||"保存失败","err");
  };
}
function openChangeRecModal(c){
  $("changeEditTitle").textContent=c?"编辑变更记录":"新建变更记录";
  const now=Math.floor(Date.now()/1000);
  const st=c&&c.status||"draft";
  const statuses=["draft","pending_approval","approved","scheduled","in_progress","completed","rolled_back","rejected","cancelled"];
  const linkChips=((c&&c.links)||[]).map(l=>`<span class="tag">${esc(l.type)}:${esc(l.id)}</span>`).join(" ");
  const wf=c&&c.id?`
    <div class="sre-toolbar" style="margin:8px 0;flex-wrap:wrap;gap:6px">
      <button type="button" class="btn sm" data-cwf="submit">提交审批</button>
      <button type="button" class="btn sm" data-cwf="approve">批准</button>
      <button type="button" class="btn sm" data-cwf="reject">驳回</button>
      <button type="button" class="btn sm" data-cwf="schedule">排期</button>
      <button type="button" class="btn sm primary" data-cwf="start">开始执行</button>
      <button type="button" class="btn sm" data-cwf="complete">完成</button>
      <button type="button" class="btn sm" data-cwf="rollback">回滚</button>
      <button type="button" class="btn danger sm" data-cwf="cancel">取消</button>
    </div>
    <div class="hint" style="margin-bottom:8px">当前状态：<b>${esc(st)}</b>${(c.approver)?` · 审批人 ${esc(c.approver)}`:""}</div>
    ${linkChips?`<div class="hint" style="margin-bottom:8px">关联：${linkChips}</div>`:""}`:"";
  $("changeEditBody").innerHTML=`
    ${wf}
    <div class="field"><label>标题</label><input id="crTitle" value="${esc(c&&c.title||"")}"></div>
    <div class="field"><label>摘要</label><textarea id="crSum" rows="2">${esc(c&&c.summary||"")}</textarea></div>
    <div class="grid2">
      <div class="field"><label>类型</label><div class="select-wrap"><select id="crKind">${["deploy","config","infra","emergency","sql","other"].map(k=>`<option value="${k}"${(c&&c.kind||"other")===k?" selected":""}>${k}</option>`).join("")}</select></div></div>
      <div class="field"><label>风险</label><div class="select-wrap"><select id="crRisk">${["low","medium","high"].map(k=>`<option value="${k}"${(c&&c.risk||"medium")===k?" selected":""}>${k}</option>`).join("")}</select></div></div>
    </div>
    <div class="grid2">
      <div class="field"><label>状态</label><div class="select-wrap"><select id="crStatus">${statuses.map(k=>`<option value="${k}"${st===k?" selected":""}>${k}</option>`).join("")}</select></div></div>
      <div class="field"><label>开始</label><input type="datetime-local" id="crStart" value="${_dtLocal(c&&c.started_at||now)}"></div>
    </div>
    <div class="field"><label>实施计划</label><textarea id="crPlan" rows="2">${esc(c&&c.plan||"")}</textarea></div>
    <div class="field"><label>回滚计划</label><textarea id="crRollback" rows="2">${esc(c&&c.rollback_plan||"")}</textarea></div>
    <div class="field"><label>验证计划</label><textarea id="crTest" rows="2">${esc(c&&c.test_plan||"")}</textarea></div>
    <div class="field"><label>作用主机</label>
      <input type="hidden" id="crHosts" value="${esc((c&&c.host_ids||[]).join(","))}">
      <div id="crHostPick"></div>
    </div>
    <div class="field"><label>关联事件 ID（逗号）</label><input id="crIncidents" value="${esc((c&&c.linked_incident_ids||[]).join(","))}"></div>
    <div class="field"><label>外链</label><input id="crRef" value="${esc(c&&c.external_ref||"")}"></div>
    <input type="hidden" id="crId" value="${c&&c.id||0}">`;
  $("changeEditMask").classList.add("show");
  (async () => {
    if (typeof loadHostFolders === "function") { try { await loadHostFolders(); } catch (_) {} }
    if ((!LAST_HOSTS || !LAST_HOSTS.length) && typeof fetchHostsList === "function") {
      try { await fetchHostsList({ force: false }); } catch (_) {}
    }
    sreMountHostMultiPick("crHostPick", "crHosts", (c && c.host_ids) || []);
  })();
  $("changeEditBody").querySelectorAll("[data-cwf]").forEach(b=>b.onclick=async()=>{
    const action=b.dataset.cwf;
    const cid=parseInt($("crId").value,10)||0;
    if(!cid) return;
    let url=`${API}/changes/${cid}/${action}`;
    let r=await fetch(url,{method:"POST"});
    let j=await r.json().catch(()=>({}));
    if(!r.ok && action==="approve" && /职责分离|自批|作者/.test(String(j.error||""))){
      const ok = typeof uiConfirm === "function"
        ? await uiConfirm({
            title: I18N.t("ui.confirm","确认"),
            message: (j.error||"职责分离拦截")+"\n\n管理员可 break-glass 强制批准（记入审计）。是否继续？",
            tone: "danger"
          })
        : confirm((j.error||"职责分离拦截")+"\n\n管理员可 break-glass 强制批准（记入审计）。是否继续？");
      if(ok){
        r=await fetch(url+(url.includes("?")?"&":"?")+"break_glass=1",{method:"POST"});
        j=await r.json().catch(()=>({}));
      } else return;
    }
    if(!r.ok){ toast(j.error||"流转失败","err"); return; }
    toast(`已${action}`,"ok"); openChangeRecModal(j); loadChanges();
  });
  $("changeEditSave").onclick=async()=>{
    const toUnix=v=>{ const t=Date.parse(v); return isNaN(t)?0:Math.floor(t/1000); };
    const body={id:parseInt($("crId").value,10)||0,title:$("crTitle").value.trim(),summary:$("crSum").value.trim(),
      kind:$("crKind").value,risk:$("crRisk").value,status:$("crStatus").value,started_at:toUnix($("crStart").value),
      plan:($("crPlan")&&$("crPlan").value||"").trim(),
      rollback_plan:($("crRollback")&&$("crRollback").value||"").trim(),
      test_plan:($("crTest")&&$("crTest").value||"").trim(),
      host_ids:($("crHosts").value||"").split(",").map(s=>s.trim()).filter(Boolean),
      linked_incident_ids:(($("crIncidents")&&$("crIncidents").value)||"").split(",").map(s=>parseInt(s.trim(),10)).filter(n=>n>0),
      external_ref:$("crRef").value.trim()};
    const r=await fetch(`${API}/changes`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    const j=await r.json().catch(()=>({}));
    if(r.ok){
      $("changeEditMask").classList.remove("show"); loadChanges(); toast("已保存","ok");
      if(j.id){ fetch(`${API}/changes/${j.id}/impact`).then(r=>r.json()).then(imp=>{
        if(imp&&(imp.services||[]).length) toast(`影响服务：${imp.services.map(s=>s.name).join(", ")}`,"ok");
      }).catch(()=>{}); }
    }
    else toast(j.error||"保存失败","err");
  };
}

/* ---- 日志检索 ---- */
const _logLvlCls = l => l==="error"?"crit":l==="warn"?"warn":"info";
// 日志检索分页状态：与概览「操作日志」的 LOG_PAGE/LOG_PAGE_SIZE（core.js）完全独立，
// 独立命名 + 独立 #logsPager 元素 + 独立 renderLogsPager，避免两个日志视图互相干扰。
let LOGS_PAGE = 1, LOGS_PAGE_SIZE = 50, LOGS_TOTAL = 0, LOGS_PAGES = 1;
let LAST_LOG_STATS = null; // 缓存上次搜索的统计数据

async function loadLogs(){
  try { if (!SRE_HOSTS.length) SRE_HOSTS=(await fetch(`${API}/hosts`).then(r=>r.json()))||[]; } catch(e){}
  const hs=$("logHost");
  if (hs && hs.options.length<=1) hs.innerHTML=`<option value="">${I18N.t("ui.all_hosts","全部主机")}</option>`+SRE_HOSTS.map(h=>`<option value="${esc(h.id)}">${esc((window.HostPicker&&HostPicker.optionLabel)?HostPicker.optionLabel(h):(h.hostname||h.id))}</option>`).join("");
  // 日志来源下拉：本地聚合 + 已接入且启用的 Loki 数据源
  const srcSel=$("logSource");
  if (srcSel) {
    const cur=srcSel.value;
    try {
      const ds=await fetch(`${API}/datasources`).then(r=>r.json());
      const loki=(Array.isArray(ds)?ds:[]).filter(d=>d.type==="loki" && d.enabled!==false);
      srcSel.innerHTML=`<option value="">${I18N.t("sre.log_local","本地聚合")}</option>`+loki.map(d=>`<option value="${esc(d.id)}">${esc(d.name)}（Loki）</option>`).join("");
      if (cur && loki.some(d=>d.id===cur)) srcSel.value=cur;
    } catch(e){}
    onLogSourceChange();
  }
  searchLogs();
}

// 切换日志来源：Loki 模式下隐藏主机/级别筛选（Loki 用自己的标签选择器），显示 Job 筛选
// 关键字框改为 LogQL 输入
function onLogSourceChange(){
  const loki=!!($("logSource") && $("logSource").value);
  const hw=$("logHostWrap"), lw=$("logLevelWrap"), kw=$("logKeyword");
  const jw=$("logJobWrap"), js=$("logJob");
  if (hw) hw.style.display=loki?"none":"";
  if (lw) lw.style.display=loki?"none":"";
  if (kw) {
    if (loki) { kw.placeholder=I18N.t("sre.logql_hint",'LogQL，如')+' {job="nginx"} |= "error"'; kw.style.width="360px"; }
    else {
      // I18N.t 在缺键时返回键名本身（真值），不能用 || 兜底，否则占位符会显示 "logs.keyword_ph"
      const ph=I18N.t("logs.keyword_ph");
      kw.placeholder=(ph && ph!=="logs.keyword_ph")?ph:I18N.t("sre.keyword_ph","关键字…");
      kw.style.width="190px";
    }
  }
  // Job 筛选：仅 Loki 模式显示，切换时自动加载 job 列表并更新关键字框
  if (jw) {
    if (loki) {
      jw.style.display="";
      if (js) { js.value=""; loadLogJobs($("logSource").value); }
      onLogJobChange();
    } else {
      jw.style.display="none";
    }
  }
  const el=$("logResults"); if (el) el.innerHTML="";
  const sp=$("logStatsPanel"); if (sp) sp.style.display="none";
  const pg=$("logsPager"); if (pg) pg.innerHTML="";
}

// 从 Loki 数据源加载 job 标签值列表
async function loadLogJobs(dsId){
  const js=$("logJob");
  if (!js || !dsId) return;
  const cur=js.value;
  js.innerHTML='<option value="">'+I18N.t("sre.all_jobs","全部 job")+'</option><option value="">'+I18N.t("sre.loading","加载中…")+'</option>';
  try {
    const resp=await fetch(`${API}/datasources/${encodeURIComponent(dsId)}/labels?label=job`).then(r=>r.json());
    const labels=(resp.ok && Array.isArray(resp.labels))?resp.labels:[];
    js.innerHTML='<option value="">'+I18N.t("sre.all_jobs","全部 job")+'</option>'+labels.map(v=>`<option value="${esc(v)}">${esc(v)}</option>`).join("");
    if (cur && labels.includes(cur)) js.value=cur;
  } catch(e) {
    js.innerHTML='<option value="">'+I18N.t("sre.all_jobs","全部 job")+'</option><option value="">'+I18N.t("sre.load_failed_manual","加载失败，请手动输入")+'</option>';
  }
}

// Job 筛选变更：自动更新 LogQL 关键字框中的 job 选择器
function onLogJobChange(){
  const js=$("logJob"), kw=$("logKeyword");
  if (!js || !kw) return;
  const job=js.value;
  if (job) {
    // 选中具体 job：更新关键字框为 {job="xxx"}
    kw.value=`{job="${job}"}`;
  } else {
    // 全部 job：匹配所有含 job 标签的日志流
    kw.value='{job=~"(.+)"}';
  }
}

// Loki 检索：把关键字框内容当 LogQL，经数据源查询接口直查，渲染成日志行
async function searchLokiLogs(dsId){
  const q=$("logKeyword").value.trim();
  const since=$("logSince").value;
  const el=$("logResults");
  if (!q) { if (el) el.innerHTML=`<div class="empty-line">${I18N.t("sre.enter_logql","请输入 LogQL，如")} {job="nginx"} |= "error"</div>`; return; }
  if (el) el.innerHTML=`<div class="empty-line">${I18N.t("sre.searching","检索中…")}</div>`;
  const sp=$("logStatsPanel"); if (sp) sp.style.display="none";
  const pg=$("logsPager"); if (pg) pg.innerHTML="";
  try {
    const body={ query:q, limit:300, since_min:(since && since!=="0")?parseInt(since):720 };
    const resp=await fetch(`${API}/datasources/${encodeURIComponent(dsId)}/query`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)}).then(r=>r.json());
    if (!resp.ok) { if (el) el.innerHTML=`<div class="empty-line">${I18N.t("sre.search_failed","检索失败")}: ${esc(resp.error||I18N.t("sre.unknown_error","未知错误"))}</div>`; return; }
    const lines=(resp.result||"").split("\n").filter(x=>x.trim());
    if (!lines.length || (lines.length===1 && lines[0].startsWith("（"))) { if (el) el.innerHTML=`<div class="empty-line">${esc(lines[0]||I18N.t("sre.no_match_logs","无匹配日志"))}</div>`; return; }
    el.innerHTML=lines.map(line=>{
      const m=line.match(/^(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d)\s+([\s\S]*)$/);
      const ts=m?m[1]:"", msg=m?m[2]:line;
      const lvl=/\b(error|err|fatal|panic|exception)\b/i.test(msg)?"error":/\b(warn|warning)\b/i.test(msg)?"warn":"info";
      return `<div class="log-line ${_logLvlCls(lvl)}">
        <span class="log-ts mono">${esc(ts)}</span>
        <span class="log-lvl ${_logLvlCls(lvl)}">${esc(lvl)}</span>
        <span class="log-msg">${esc(msg)}</span>
      </div>`;
    }).join("");
  } catch(e){ if (el) el.innerHTML=`<div class="empty-line">${I18N.t("sre.search_failed","检索失败")}: ${esc(e)}</div>`; }
}

async function searchLogs(page){
  // Loki 数据源模式：走 LogQL 直查，不用本地聚合的分页/筛选
  const srcSel=$("logSource");
  if (srcSel && srcSel.value) { return searchLokiLogs(srcSel.value); }
  if (page !== undefined) { LOGS_PAGE = page; } else { LOGS_PAGE = 1; }
  const host=$("logHost").value,level=$("logLevel").value,since=$("logSince").value,kw=$("logKeyword").value.trim();
  const qs=new URLSearchParams();
  if(host)qs.set("host",host); if(level)qs.set("level",level);
  if(since&&since!=="0")qs.set("since_min",since); if(kw)qs.set("q",kw);
  qs.set("page",String(LOGS_PAGE)); qs.set("page_size",String(LOGS_PAGE_SIZE));
  try {
    const resp=await fetch(`${API}/logs?${qs}`).then(r=>r.json());
    const items=resp.items||[]; LOGS_TOTAL=resp.total||0; LOGS_PAGES=resp.pages||1;
    LAST_LOG_STATS = resp.stats || null;

    // 渲染统计面板
    renderLogStats(resp.stats, resp.total);

    // 渲染日志列表
    const el=$("logResults");
    if(!items.length){ el.innerHTML=`<div class="empty-line">${I18N.t("sre.no_match_logs_hint","无匹配日志（被控端需以 --log-paths 指定采集文件）")}</div>`; renderLogsPager(); return; }
    el.innerHTML=items.map(l=>`<div class="log-line ${_logLvlCls(l.level)}">
      <span class="log-ts mono">${fmtDateTime(l.ts)}</span>
      <span class="log-lvl ${_logLvlCls(l.level)}">${esc(l.level)}</span>
      <span class="log-host">${esc(l.hostname)}</span>
      <span class="log-msg">${esc(l.message)}</span>
      ${(l.level==="error"||l.level==="warn")?`<button class="log-diag-btn" data-log='${esc(JSON.stringify({ts:l.ts,hostname:l.hostname,host_id:l.host_id||"",level:l.level,message:l.message}))}' title="${I18N.t("sre.submit_diag","提交诊断")}">🔍</button>`:""}
    </div>`).join("");

    // 绑定单条日志诊断按钮
    el.querySelectorAll(".log-diag-btn").forEach(b=>{ b.onclick=function(e){ e.stopPropagation(); const d=JSON.parse(this.dataset.log); diagnoseLogLine(d); }; });

    // 渲染分页控件
    renderLogsPager();
  } catch(e){ toast(I18N.t("sre.search_failed","检索失败")+": "+e,"err"); }
}

// 渲染日志统计面板
function renderLogStats(stats, total){
  const panel=$("logStatsPanel");
  if(!panel) return;
  if(!stats || !total){
    // 空态也保留看板结构，避免用户以为功能缺失；并提示数据来源
    // 注意：.log-stats 默认 display:none，须显式设为可见值（""会回落到 CSS 的 none）
    panel.style.display="block";
    panel.innerHTML=`<div class="log-stats-bar"><div class="log-stats-left"><span class="log-stat-total">${I18N.t("sre.total_prefix","共")} <strong>0</strong> ${I18N.t("sre.count_unit","条")}</span><span class="log-stat-empty">${I18N.t("sre.log_empty_hint","暂无匹配日志——被控端需在安装时以 --log-paths 指定采集文件；或放宽上方筛选条件后重试")}</span></div></div>`;
    return;
  }
  panel.style.display="block"; // 显式可见（.log-stats 默认 display:none，""会回落到 none）
  const byLvl=stats.by_level||{};
  const topHosts=stats.top_hosts||[];
  const timeDist=stats.time_distribution||{};

  // 按级别统计
  let levelHTML="";
  ["error","warn","info","debug"].forEach(lv=>{
    const cnt=byLvl[lv]||0;
    if(cnt>0 || lv==="error" || lv==="warn"){
      levelHTML+=`<span class="log-stat-chip ${_logLvlCls(lv)}">${lv}: <strong>${cnt}</strong></span>`;
    }
  });

  // 按主机 Top 5 — 横向柱状图可视化
  let hostHTML="";
  if(topHosts.length){
    const maxCount=topHosts[0].count||1;
    const barColors=['#4c8dff','#06b6d4','#8b5cf6','#22c55e','#f59e0b'];
    hostHTML='<div class="log-stat-row"><span class="log-stat-label">'+I18N.t("sre.top_hosts_label","Top 主机：")+'</span><div class="log-top-host-bars">';
    topHosts.forEach((h,i)=>{
      const pct=Math.round((h.count/maxCount)*100);
      const color=barColors[i%barColors.length];
      hostHTML+=`<div class="log-top-host-item" data-host="${esc(h.hostname)}" title="${esc(h.hostname)}：${h.count} ${I18N.t("sre.logs_unit","条日志")}">
        <span class="log-top-host-name">${esc(h.hostname)}</span>
        <div class="log-top-host-track"><div class="log-top-host-fill" style="width:${pct}%;background:${color}"></div></div>
        <span class="log-top-host-count" style="color:${color}">${h.count}</span>
      </div>`;
    });
    hostHTML+='</div></div>';
  }

  // 时间分布
  const h1=timeDist["1h"]||0, h6=timeDist["6h"]||0, h24=timeDist["24h"]||0;
  const timeHTML=`<span class="log-stat-chip time">${I18N.t("sre.recent","近")}1h: <strong>${h1}</strong></span><span class="log-stat-chip time">${I18N.t("sre.recent","近")}6h: <strong>${h6}</strong></span><span class="log-stat-chip time">${I18N.t("sre.recent","近")}24h: <strong>${h24}</strong></span>`;

  // 一键诊断按钮（error > 10 条且 since_min <= 30）
  const errCount=byLvl["error"]||0;
  const sinceVal=$("logSince").value;
  const showDiag=errCount>=10 && (sinceVal==="15"||sinceVal==="30"||sinceVal==="60"||!sinceVal||sinceVal==="0");
  const diagBtn=showDiag ? `<button class="btn warn sm" id="logDiagBtn" style="margin-left:auto">⚡ ${I18N.t("sre.one_click_diag","一键诊断")}（${errCount} ${I18N.t("sre.errors_unit","条错误")}）</button>` : "";

  panel.innerHTML=`<div class="log-stats-bar">
    <div class="log-stats-left">
      <span class="log-stat-total">${I18N.t("sre.total_prefix","共")} <strong>${total}</strong> ${I18N.t("sre.count_unit","条")}</span>
      ${levelHTML}
    </div>
    ${diagBtn}
  </div>
  ${hostHTML}
  <div class="log-stat-row"><span class="log-stat-label">${I18N.t("sre.time_dist","时间分布：")}</span>${timeHTML}</div>`;

  // 绑定 Top 主机点击筛选
  panel.querySelectorAll(".log-top-host-item").forEach(item=>{
    item.onclick=()=>{
      const hostSel=$("logHost");
      if(!hostSel) return;
      const hn=item.dataset.host;
      for(let i=0;i<hostSel.options.length;i++){
        if(hostSel.options[i].textContent===hn){ hostSel.value=hostSel.options[i].value; break; }
      }
      searchLogs(1);
    };
  });

  // 绑定一键诊断
  const diagBtnEl=$("logDiagBtn");
  if(diagBtnEl){
    diagBtnEl.onclick=()=>{
      const host=$("logHost").value, hostname=$("logHost").selectedOptions[0]?.textContent||"";
      const since=$("logSince").value;
      diagnoseBulkLogs(host, hostname, parseInt(since)||60);
    };
  }
}

// 渲染日志分页控件
function renderLogsPager(){
  const pager=$("logsPager");
  if(!pager) return;
  if(LOGS_TOTAL===0){ pager.innerHTML=`<span class="pinfo">${I18N.t("sre.total_prefix","共")} 0 ${I18N.t("sre.count_unit","条")}</span>`; return; }
  if(LOGS_PAGES<=1){ pager.innerHTML=`<span class="pinfo">${I18N.t("sre.total_prefix","共")} ${LOGS_TOTAL} ${I18N.t("sre.count_unit","条")}</span>`; return; }
  let btns=`<button ${LOGS_PAGE===1?"disabled":""} data-lpg="prev">‹</button>`;
  for(let i=1;i<=LOGS_PAGES;i++){
    if(i===1||i===LOGS_PAGES||Math.abs(i-LOGS_PAGE)<=1){
      btns+=`<button class="${i===LOGS_PAGE?"active":""}" data-lpg="${i}">${i}</button>`;
    }else if(Math.abs(i-LOGS_PAGE)===2){
      btns+=`<span class="pinfo">…</span>`;
    }
  }
  btns+=`<button ${LOGS_PAGE===LOGS_PAGES?"disabled":""} data-lpg="next">›</button>`;
  btns+=`<span class="pinfo">${I18N.t("sre.total_prefix","共")} ${LOGS_TOTAL} ${I18N.t("sre.count_unit","条")} · ${LOGS_PAGE}/${LOGS_PAGES} ${I18N.t("sre.page_unit","页")}</span>`;
  pager.innerHTML=btns;

  // 绑定分页按钮事件
  pager.querySelectorAll("[data-lpg]").forEach(b=>{
    b.onclick=()=>{
      const v=b.dataset.lpg;
      if(v==="prev"){ if(LOGS_PAGE>1) searchLogs(LOGS_PAGE-1); }
      else if(v==="next"){ if(LOGS_PAGE<LOGS_PAGES) searchLogs(LOGS_PAGE+1); }
      else{ const p=parseInt(v); if(p>0&&p<=LOGS_PAGES) searchLogs(p); }
    };
  });
}

// 一键诊断：批量错误日志
async function diagnoseBulkLogs(hostID, hostname, sinceMin){
  toast(I18N.t("sre.diagnosing","正在诊断…"),"ok");
  try {
    const r=await fetch(`${API}/logs/diagnose`,{
      method:"POST",
      headers:{"Content-Type":"application/json"},
      body:JSON.stringify({host_id:hostID,hostname:hostname,since_min:sinceMin})
    });
    if(!r.ok){ toast(I18N.t("sre.diag_req_failed","诊断请求失败")+": "+r.status,"err"); return; }
    const rep=await r.json();
    // 显示诊断结果
    showDiagnosisResult(rep);
  } catch(e){ toast(I18N.t("sre.diagnose_failed","诊断失败")+": "+e,"err"); }
}

// 单条日志诊断
async function diagnoseLogLine(log){
  toast(I18N.t("sre.diagnosing","正在诊断…"),"ok");
  try {
    const r=await fetch(`${API}/logs/diagnose`,{
      method:"POST",
      headers:{"Content-Type":"application/json"},
      body:JSON.stringify({
        host_id:log.host_id||"",
        hostname:log.hostname||"",
        since_min:30,
        single_log:`[${log.level}] ${log.hostname} ${fmtDateTime(log.ts)} ${log.message}`
      })
    });
    if(!r.ok){ toast(I18N.t("sre.diag_req_failed","诊断请求失败")+": "+r.status,"err"); return; }
    const rep=await r.json();
    showDiagnosisResult(rep);
  } catch(e){ toast(I18N.t("sre.diagnose_failed","诊断失败")+": "+e,"err"); }
}

// 显示诊断结果
function showDiagnosisResult(rep){
  const panel=$("logDiagResult");
  if(!panel) return;
  const src=rep.source==="ai"?I18N.t("sre.ai_verdict","AI 研判"):I18N.t("sre.rule_diag","规则诊断");
  const srcCls=rep.source==="ai"?"info":"";
  const findings=(rep.findings||[]).map(f=>`<div class="ai-finding"><span class="badge ${f.severity==="critical"?"crit":"warn"}">${esc(f.severity)}</span><div class="ai-f-body"><div class="ai-f-title">${esc(f.title)}</div>${f.detail?`<div class="ai-f-detail">${esc(f.detail)}</div>`:""}</div></div>`).join("");
  panel.innerHTML=`<div class="log-diag-card">
    <div class="log-diag-head"><span>🔍 ${I18N.t("sre.diag_result","诊断结果")}</span><span class="badge ${srcCls}" style="margin-left:8px">${esc(src)}${rep.model?" · "+esc(rep.model):""}</span><button class="log-diag-close" title="${I18N.t("assist.close","关闭")}">✕</button></div>
    <div class="log-diag-summary">${esc(rep.summary||"")}</div>
    ${findings?`<div class="ai-findings">${findings}</div>`:""}
    ${rep.context?`<div class="log-diag-ctx">${esc(rep.context)}</div>`:""}
  </div>`;
  // CSP 禁内联 onclick：渲染后再绑定关闭（此前 onclick="..." 会被 script-src 'self' 拦截而失效）
  const closeBtn=panel.querySelector(".log-diag-close");
  if(closeBtn) closeBtn.onclick=()=>{ panel.innerHTML=""; };
  panel.scrollIntoView({behavior:"smooth",block:"nearest"});
}
/* ---- AI 巡检 ---- */
async function loadInspections(){
  try {
    const list=await fetch(`${API}/ai/inspections`).then(r=>r.json());
    const el=$("aiReportList");
    if(!list||!list.length){ el.innerHTML=`<div class="empty-line">${I18N.t("sre.no_inspections","暂无巡检报告，点「立即巡检」生成一次。")}</div>`; return; }
    el.innerHTML=list.map(rep=>{
      const f=(rep.findings||[]).map(x=>`<div class="ai-finding"><span class="badge ${_sevCls(x.severity)}">${esc(x.severity)}</span><div class="ai-f-body"><div class="ai-f-title">${esc(x.title)}</div>${x.detail?`<div class="ai-f-detail">${esc(x.detail)}</div>`:""}</div></div>`).join("");
      const meta=[rep.model?esc(rep.model):"",(typeof rep.duration_ms==="number"&&rep.duration_ms>=0)?rep.duration_ms+"ms":""].filter(Boolean).join(" · ");
      return `<div class="ai-report"><div class="ai-report-head"><span class="badge ${rep.source==="ai"?"info":""}">${rep.source==="ai"?I18N.t("sre.ai_verdict","AI 研判"):I18N.t("sre.heuristic","启发式")}</span><span class="ai-report-trigger">${rep.trigger==="manual"?I18N.t("sre.src_manual","手动"):I18N.t("sre.sched_scheduled","定时")}</span>${meta?`<span class="mono" style="color:var(--muted2);font-size:11px">${meta}</span>`:""}<span class="mono" style="color:var(--muted);margin-left:auto">${fmtDateTime(rep.ts)}</span></div>
        ${rep.context?`<div class="ai-report-ctx">${esc(rep.context)}</div>`:""}
        <div class="ai-summary">${esc(rep.summary)}</div>${f?`<div class="ai-findings">${f}</div>`:""}</div>`;
    }).join("");
  } catch(e){ toast(I18N.t("sre.load_failed","加载失败")+": "+e,"err"); }
}
async function runInspect(){ toast(I18N.t("sre.inspecting","巡检中…"),"ok"); try { await fetch(`${API}/ai/inspect`,{method:"POST"}); loadInspections(); } catch(e){ toast(I18N.t("sre.inspect_failed","巡检失败")+": "+e,"err"); } }
// AI 技能库：查看/删除自进化提炼的技能，手动触发提炼
async function openSkills(){
  const m=$("skillsMask"); if(m) m.classList.add("show");
  await loadSkills();
}
async function loadSkills(){
  const body=$("skillsBody"); if(!body) return;
  body.innerHTML=`<div class="empty-line" style="padding:16px">${I18N.t("sre.loading","加载中…")}</div>`;
  try{
    const showArchived=!!$("skillsShowArchived")?.checked;
    const skills=await fetch(`${API}/ai/skills?archived=${showArchived?1:0}`).then(r=>r.json());
    if(!skills||!skills.length){
      body.innerHTML=`<div class="empty-line" style="padding:20px">${I18N.t("sre.skills_empty","还没有技能。随着 AI 诊断 / 剧本执行 / 事件解决 的经验积累，系统每日会自动从中提炼可复用技能；也可点右上角「立即提炼」。")}</div>`;
      return;
    }
    body.innerHTML=`<div class="skill-list">`+skills.map(s=>{
      const succ=s.use_count>0?Math.min(100,Math.round((s.success_count/s.use_count)*100)):0;
      const st=s.status||"active";
      const archived=st==="archived";
      const draft=st==="draft";
      const stBadge=draft?`<span class="badge warn">draft</span>`:(archived?`<span class="badge">archived</span>`:`<span class="badge ok">active</span>`);
      const scope=[s.service_ids?`svc:${s.service_ids}`:"",s.categories?`cat:${s.categories}`:""].filter(Boolean).join(" · ")||"全局";
      return `<div class="skill-card${archived?" skill-archived":""}${draft?" skill-draft":""}">
        <div class="skill-head"><b>${esc(s.name)}</b> ${stBadge}
          <span class="skill-meta">v${s.version||1} · ${I18N.t("sre.skill_used","用")} ${s.use_count} · ${I18N.t("sre.skill_success","成功")} ${succ}% · ${I18N.t("sre.skill_weight","权重")} ${(s.priority||1).toFixed(1)}${s.source==="manual"?" · "+I18N.t("sre.skill_manual","手工"):(String(s.source||"").startsWith("pack:")?" · 知识包":(String(s.source||"").startsWith("customer:")?" · 客户包":""))}</span>
          ${draft?`<button class="btn sm primary" data-skill-activate="${s.id}">激活</button>`:""}
          ${archived
            ?`<button class="btn sm" data-skill-restore="${s.id}">恢复</button>`
            :`<button class="btn sm" data-skill-archive="${s.id}">归档</button>`}
          <button class="btn sm" data-skill-scope="${s.id}" data-svc="${esc(s.service_ids||"")}" data-cat="${esc(s.categories||"")}">作用域</button>
          <button class="btn danger sm" data-skill-del="${s.id}">${I18N.t("ui.delete","删除")}</button></div>
        <div class="skill-trigger">${I18N.t("sre.skill_applies","适用：")}${esc(s.trigger||"")}</div>
        <div class="hint">作用域：${esc(scope)}</div>
        <pre class="skill-steps">${esc(s.steps||"")}</pre>
        ${s.tags?`<div class="skill-tags">${esc(s.tags)}</div>`:""}
      </div>`;
    }).join("")+`</div>`;
    body.querySelectorAll("[data-skill-del]").forEach(b=>b.onclick=async()=>{
      if(!confirm(I18N.t("sre.confirm_del_skill","删除该技能？"))) return;
      await fetch(`${API}/ai/skills/${b.dataset.skillDel}`,{method:"DELETE"});
      loadSkills();
    });
    body.querySelectorAll("[data-skill-archive]").forEach(b=>b.onclick=async()=>{
      await fetch(`${API}/ai/skills/${b.dataset.skillArchive}/archive`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({status:"archived"})});
      loadSkills();
    });
    body.querySelectorAll("[data-skill-restore]").forEach(b=>b.onclick=async()=>{
      await fetch(`${API}/ai/skills/${b.dataset.skillRestore}/archive`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({status:"active"})});
      loadSkills();
    });
    body.querySelectorAll("[data-skill-activate]").forEach(b=>b.onclick=async()=>{
      await fetch(`${API}/ai/skills/${b.dataset.skillActivate}/archive`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({status:"active"})});
      toast("已激活，将参与检索","ok");
      loadSkills();
    });
    body.querySelectorAll("[data-skill-scope]").forEach(b=>b.onclick=async()=>{
      const svc=prompt("业务服务 ID（逗号，空=全局）", b.dataset.svc||"");
      if(svc===null) return;
      const cat=prompt("主机分类（逗号，空=全局）", b.dataset.cat||"");
      if(cat===null) return;
      await fetch(`${API}/ai/skills/${b.dataset.skillScope}/scope`,{method:"POST",headers:{"Content-Type":"application/json"},
        body:JSON.stringify({service_ids:svc.trim(),categories:cat.trim()})});
      toast("作用域已更新","ok");
      loadSkills();
    });
  }catch(e){ body.innerHTML=`<div class="empty-line" style="padding:16px">${I18N.t("sre.load_failed","加载失败")}：${esc(String(e))}</div>`; }
}
async function exportCustomerSkills(){
  try{
    const r=await fetch(`${API}/ai/skills/export?status=active`);
    if(!r.ok){ const j=await r.json().catch(()=>({})); toast(j.error||"导出失败","err"); return; }
    const blob=await r.blob();
    const a=document.createElement("a");
    a.href=URL.createObjectURL(blob);
    a.download="customer-skills.json";
    a.click();
    URL.revokeObjectURL(a.href);
    toast("客户技能包已导出","ok");
  }catch(e){ toast(String(e),"err"); }
}
async function importCustomerSkillsFile(file){
  if(!file) return;
  try{
    const text=await file.text();
    const r=await fetch(`${API}/ai/skills/import`,{method:"POST",headers:{"Content-Type":"application/json"},body:text});
    const j=await r.json().catch(()=>({}));
    if(!r.ok){ toast(j.error||"导入失败","err"); return; }
    toast(`导入完成：${j.imported||0} 条（默认 draft，需激活）`,"ok");
    loadSkills();
  }catch(e){ toast(String(e),"err"); }
}
async function distillSkillsNow(){
  toast(I18N.t("sre.distilling","提炼中，请稍候…"),"ok");
  try{
    const j=await fetch(`${API}/ai/skills/distill`,{method:"POST"}).then(r=>r.json());
    if(j.ok) toast(`${I18N.t("sre.distill_done","提炼完成，新增")} ${j.created||0} ${I18N.t("sre.skills_unit","条技能")}`,"ok"); else toast(I18N.t("sre.distill_failed","提炼失败")+"："+(j.error||I18N.t("sre.unknown","未知")),"err");
    loadSkills();
  }catch(e){ toast(I18N.t("sre.distill_failed","提炼失败")+"："+e,"err"); }
}
async function importSkillPacksNow(){
  toast("正在导入行业知识包…","ok");
  try{
    const j=await fetch(`${API}/ai/skill-packs/import`,{method:"POST",headers:{"Content-Type":"application/json"},body:"{}"}).then(r=>r.json());
    if(j.imported_total!=null) toast(`知识包导入完成：更新/新增 ${j.imported_total} 条技能`,"ok");
    else toast(j.error||"导入失败","err");
    loadSkills();
  }catch(e){ toast("导入失败："+e,"err"); }
}

async function openCopilot(){
  const m=$("copilotMask"); if(m) m.classList.add("show");
  await loadCopilot();
}
async function loadCopilot(){
  const body=$("copilotBody"); if(!body) return;
  body.innerHTML=`<div class="empty-line" style="padding:16px">${I18N.t("sre.loading","加载中…")}</div>`;
  try{
    const j=await fetch(`${API}/ai/copilot/context`).then(r=>r.json());
    const incs=j.open_incidents||[];
    const rem=j.pending_remediation||[];
    const packs=j.skill_packs||[];
    const sug=j.suggestions||[];
    const hints=j.skill_hints||[];
    body.innerHTML=`
      <div class="grid2" style="gap:12px">
        <div>
          <h4 style="margin:0 0 8px">未决事件（${incs.length}）</h4>
          ${incs.length?`<div class="skill-list">${incs.map(i=>`<div class="skill-card">
            <div class="skill-head"><b>#${i.id} [${esc(i.severity)}]</b>
              <button class="btn sm ai-assist-btn" data-copilot-diag="${i.id}">AI 诊断</button></div>
            <div class="skill-trigger">${esc(i.title||"")} · ${esc(i.host||"-")} · ${esc(i.status||"")}</div>
          </div>`).join("")}</div>`:`<div class="empty-line">暂无未决事件</div>`}
          <h4 style="margin:16px 0 8px">待审批修复（${rem.length}）</h4>
          ${rem.length?`<ul style="margin:0;padding-left:18px;font-size:13px">${rem.map(r=>`<li>${esc(r.rule||"")} → ${esc(r.playbook||"")}（${esc(r.host||"")}）</li>`).join("")}</ul>`:`<div class="empty-line">无待审批项</div>`}
        </div>
        <div>
          <h4 style="margin:0 0 8px">建议动作</h4>
          <ul style="margin:0;padding-left:18px;font-size:13px">${sug.map(s=>`<li>${esc(s)}</li>`).join("")}</ul>
          <h4 style="margin:16px 0 8px">相关技能提示</h4>
          ${hints.length?`<div class="skill-tags">${hints.map(h=>esc(h.name||"")).join(" · ")}</div>`:`<div class="empty-line">暂无命中技能</div>`}
          <h4 style="margin:16px 0 8px">行业知识包</h4>
          <div class="skill-list">${packs.map(p=>`<div class="skill-card"><div class="skill-head"><b>${esc(p.name||p.id)}</b>
            <button class="btn sm" data-pack-import="${esc(p.id)}">导入</button></div>
            <div class="skill-trigger">v${esc(p.version||"")} · ${p.skill_count||0} 条</div></div>`).join("")}</div>
        </div>
      </div>
      <details style="margin-top:12px"><summary>态势原文</summary><pre class="skill-steps">${esc(j.duty_context||"")}</pre></details>`;
    body.querySelectorAll("[data-copilot-diag]").forEach(b=>b.onclick=()=>{
      const id=b.dataset.copilotDiag;
      if(typeof openIncidentDiagnose==="function") openIncidentDiagnose(+id);
      else if(typeof diagnoseIncident==="function") diagnoseIncident(+id);
      else toast("请到事件页打开 #"+id+" 诊断","warn");
    });
    body.querySelectorAll("[data-pack-import]").forEach(b=>b.onclick=async()=>{
      const id=b.dataset.packImport;
      const r=await fetch(`${API}/ai/skill-packs/import`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({id})});
      const jj=await r.json().catch(()=>({}));
      toast(jj.imported_total!=null?`已导入 ${id}（${jj.imported_total}）`:(jj.error||"失败"), jj.imported_total!=null?"ok":"err");
    });
  }catch(e){ body.innerHTML=`<div class="empty-line">加载失败：${esc(String(e))}</div>`; }
}
async function genCopilotBrief(){
  let j={};
  try{ j=await fetch(`${API}/ai/copilot/context`).then(r=>r.json()); }
  catch(e){ toast("拉取态势失败："+e,"err"); return; }
  openAIAssist({
    task:"generic",
    title:"值班助手简报",
    mode:"analyze",
    context: (j.duty_context||"") + "\n\n建议：" + (j.suggestions||[]).join("；"),
    hint:"基于当前未决事件与待审批项生成可执行值班简报"
  });
}

// AI 记忆浏览器：只读列表 + 按 kind 过滤 + 删除
async function openMemories(){
  const m=$("memoryMask"); if(m) m.classList.add("show");
  await loadMemories();
}
async function loadMemories(){
  const body=$("memoryBody"), statsEl=$("memoryStats");
  if(!body) return;
  body.innerHTML=`<div class="empty-line" style="padding:16px">${I18N.t("sre.loading","加载中…")}</div>`;
  const kind=($("memoryKindFilter")&&$("memoryKindFilter").value)||"";
  const verified=($("memoryVerifiedFilter")&&$("memoryVerifiedFilter").value)||"";
  try{
    const q=new URLSearchParams({limit:"50"});
    if(kind) q.set("kind",kind);
    if(verified) q.set("verified",verified);
    const j=await fetch(`${API}/ai/memories?${q}`).then(r=>r.json());
    const items=j.items||[];
    const stats=j.stats||{};
    if(statsEl){
      const parts=Object.keys(stats).sort().map(k=>`${k} ${stats[k]}`);
      const verN=typeof j.verified_count==="number"?` · 已验证 ${j.verified_count}`:"";
      statsEl.textContent=parts.length?`共 ${j.total||0} 条${verN} · ${parts.join(" · ")}`:`共 ${j.total||0} 条（需 PostgreSQL）`;
    }
    if(!items.length){
      body.innerHTML=`<div class="empty-line" style="padding:20px">${I18N.t("sre.memory_empty","还没有记忆。启用 AI 并完成若干诊断/对话后，经验会沉淀到此；未配置 PostgreSQL 时不可用。")}</div>`;
      return;
    }
    body.innerHTML=`<div class="skill-list">`+items.map(m=>{
      const when=m.created_at?fmtDateTime(m.created_at):"";
      const hit=m.last_hit_at?` · 命中 ${fmtDateTime(m.last_hit_at)}`:"";
      const scope=[m.service_id,m.category].filter(Boolean).join("/")||"";
      const badges=(m.verified?`<span class="badge ok">已验证</span>`:`<span class="badge">未验证</span>`)+
        (scope?` <span class="badge">${esc(scope)}</span>`:"");
      return `<div class="skill-card${m.verified?" skill-verified":""}">
        <div class="skill-head"><b>${esc(m.kind||"?")}</b> ${badges}
          <span class="skill-meta">${esc(m.source||"")} · 权重 ${(m.priority||1).toFixed(1)}${when?" · "+when:""}${hit}</span>
          <button class="btn danger sm" data-mem-del="${m.id}">${I18N.t("ui.delete","删除")}</button></div>
        <pre class="skill-steps">${esc(m.content||"")}</pre>
      </div>`;
    }).join("")+`</div>`;
    body.querySelectorAll("[data-mem-del]").forEach(b=>b.onclick=async()=>{
      if(!confirm(I18N.t("sre.confirm_del_memory","删除该记忆？"))) return;
      await fetch(`${API}/ai/memories/${b.dataset.memDel}`,{method:"DELETE"});
      loadMemories();
    });
  }catch(e){ body.innerHTML=`<div class="empty-line" style="padding:16px">${I18N.t("sre.load_failed","加载失败")}：${esc(String(e))}</div>`; }
}

async function loadAIStats(){
  const el=$("aiStatsBody"); if(!el) return;
  const days=parseInt(($("aiStatsRange")&&$("aiStatsRange").value)||"7",10)||7;
  const anchorKey="ai-usage:"+days;
  const win=(typeof resolveAnchoredRange==="function")
    ? resolveAnchoredRange(anchorKey, days*24, null)
    : { from: Math.floor(Date.now()/1000)-days*86400, to: Math.floor(Date.now()/1000) };
  const from=win.from, to=win.to;
  const load=(typeof beginRangeLoad==="function")
    ? beginRangeLoad(anchorKey)
    : { signal:undefined, isCurrent:()=>true };
  try{
    const fo=load.signal?{signal:load.signal}:undefined;
    const [j, hist, byUser]=await Promise.all([
      fetch(`${API}/ai/stats?days=${days}`, fo).then(r=>r.json()),
      fetch(`${API}/ai/usage/history?from=${from}&to=${to}`, fo).then(r=>r.json()).catch(()=>({points:[]})),
      fetch(`${API}/ai/usage/by-user?from=${from}&to=${to}&limit=15`, fo).then(r=>r.json()).catch(()=>({users:[]})),
    ]);
    if(!load.isCurrent()) return;
    const total=j.total||0, fail=j.fail||0;
    const rate=total?((j.fail_rate||0)*100).toFixed(1):"0.0";
    const avg=j.avg_latency_ms||0;
    const tok=j.approx_tokens_total||0;
    const cost=j.cost_total||0;
    const cur=j.cost_currency||hist.cost_currency||"CNY";
    const persisted=j.persisted!==false;
    const fbTotal=Number(j.feedback_total)||0;
    const fbApplied=Number(j.feedback_applied)||0;
    const fbHelpful=Number(j.feedback_helpful)||0;
    const fbUnhelpful=Number(j.feedback_unhelpful)||0;
    const fbPositive=fbTotal?((Number(j.feedback_positive_rate)||0)*100).toFixed(1):"0.0";
    const by=j.by_task||{};
    const taskRows=Object.keys(by).sort().map(k=>{
      const t=by[k];
      return `<tr><td class="mono">${esc(k)}</td><td>${t.count||0}</td><td>${t.fail||0}</td><td>${t.avg_ms||0} ms</td></tr>`;
    }).join("");
    const byModel=j.by_model||{};
    const modelKeys=Object.keys(byModel).sort((a,b)=>(byModel[b].count||0)-(byModel[a].count||0));
    const modelMax=modelKeys.length?(byModel[modelKeys[0]].count||1):1;
    const modelBars=modelKeys.slice(0,8).map(k=>{
      const t=byModel[k]||{};
      const pct=Math.max(4, Math.round(((t.count||0)/modelMax)*100));
      return `<div style="margin:4px 0"><div class="mono" style="font-size:11px;display:flex;justify-content:space-between"><span>${esc(k)}</span><span>${t.count||0} · ${(t.cost||0).toFixed?Number(t.cost||0).toFixed(4):(t.cost||0)} ${esc(cur)}</span></div>
        <div style="height:6px;background:var(--border);border-radius:3px;overflow:hidden"><div style="width:${pct}%;height:100%;background:var(--accent,#4c8dff)"></div></div></div>`;
    }).join("");
    const recent=(j.recent||[]).slice(0,8).map(r=>{
      const st=r.ok?"ok":"err";
      const who=r.actor?` · ${esc(r.actor)}`:"";
      const tokN=(r.prompt_tokens||0)+(r.completion_tokens||0)||(r.approx_tokens||0);
      return `<div class="mono" style="font-size:11px;color:var(--muted);margin:2px 0"><span class="badge ${st}">${r.ok?"OK":"FAIL"}</span> ${esc(r.task||"")} · ${esc(r.model||"")} · ${r.latency_ms||0}ms · ${tokN} tok${who}${r.error?" · "+esc(r.error):""}</div>`;
    }).join("");
    const users=(byUser.users||[]).map(u=>
      `<tr><td>${esc(u.actor||"")}</td><td>${u.calls||0}</td><td>${u.tokens||0}</td><td>${(u.cost||0).toFixed(4)} ${esc(cur)}</td></tr>`
    ).join("");
    const feedbackRows=Object.entries(j.feedback_by_task||{})
      .sort((a,b)=>(b[1].total||0)-(a[1].total||0))
      .map(([task,f])=>{
        const n=Number(f.total)||0;
        const pos=n?(((Number(f.applied)||0)+(Number(f.helpful)||0))*100/n).toFixed(1):"0.0";
        return `<tr><td class="mono">${esc(task)}</td><td>${n}</td><td>${f.applied||0}</td><td>${f.helpful||0}</td><td>${f.unhelpful||0}</td><td>${pos}%</td></tr>`;
      }).join("");
    let expTable="";
    try{
      const exp=await fetch(`${API}/ai/experiments/stats?days=${days}`).then(r=>r.json()).catch(()=>null);
      if(exp&&exp.variants&&exp.variants.length){
        expTable=`<div class="hint" style="margin-top:10px">A/B 实验 ${esc(exp.experiment_id||"")} · helpful 率</div>
          <table class="hv-mini-table" style="width:100%;margin-bottom:8px"><thead><tr><th>变体</th><th>样本</th><th>有用</th><th>需改进</th><th>采纳</th><th>helpful率</th></tr></thead><tbody>
          ${exp.variants.map(v=>`<tr><td class="mono">${esc(v.variant)}</td><td>${v.total||0}</td><td>${v.helpful||0}</td><td>${v.unhelpful||0}</td><td>${v.applied||0}</td><td>${((v.helpful_rate||0)*100).toFixed(1)}%</td></tr>`).join("")}
          </tbody></table>`;
      }
    }catch(_e){}
    let auditCard="";
    try{
      const av=await fetch(`${API}/audit/verify-chain?limit=200`).then(r=>r.json()).catch(()=>null);
      if(av){
        const deg=av.secret_degraded?" · 密钥降级(未设 AIOPS_SECRET_KEY)":"";
        auditCard=`<div class="hint" style="margin:8px 0">审计链：${av.ok?"完整":"异常"} · 已校验 ${av.checked||0}${deg}${av.detail&&!av.ok?" · "+esc(av.detail):""}</div>`;
      }
    }catch(_e){}
    const tco=j.tco||{};
    const dailyAvg=Number(tco.daily_avg_cost!=null?tco.daily_avg_cost:(cost/Math.max(1,days)))||0;
    const byTaskCost=j.by_task_cost||{};
    const taskCostRows=Object.keys(byTaskCost).sort((a,b)=>(byTaskCost[b].cost||0)-(byTaskCost[a].cost||0)).slice(0,12).map(k=>{
      const t=byTaskCost[k]||{};
      return `<tr><td class="mono">${esc(k)}</td><td>${t.count||0}</td><td>${t.tokens||0}</td><td>${(t.cost||0).toFixed(4)} ${esc(cur)}</td></tr>`;
    }).join("");
    const modelCostTotal=Object.values(byModel).reduce((s,m)=>s+(Number(m.cost)||0),0)||1;
    const modelCostBars=modelKeys.slice(0,8).map(k=>{
      const t=byModel[k]||{};
      const pct=Math.max(4, Math.round(((Number(t.cost)||0)/modelCostTotal)*100));
      return `<div style="margin:4px 0"><div class="mono" style="font-size:11px;display:flex;justify-content:space-between"><span>${esc(k)}</span><span>${(Number(t.cost)||0).toFixed(4)} ${esc(cur)} (${pct}%)</span></div>
        <div style="height:6px;background:var(--border);border-radius:3px;overflow:hidden"><div style="width:${pct}%;height:100%;background:#f97316"></div></div></div>`;
    }).join("");
    // 模型路由原因（by_route_reason）：展示每次调用「为什么选这个模型」，验证路由是否如配置所愿。
    const routeNames={task_models:"任务映射",cheap_model:"廉价模型",primary:"主模型",fallback:"故障转移",unknown:"未知",none:"未记录"};
    const byRoute=j.by_route_reason||{};
    const routeKeys=Object.keys(byRoute).sort((a,b)=>(byRoute[b].count||0)-(byRoute[a].count||0)).slice(0,6);
    const routeRows=routeKeys.map(k=>{
      const t=byRoute[k]||{};
      const label=routeNames[k]||esc(k);
      const pct=total?Math.round(((t.count||0)/total)*100):0;
      return `<tr><td>${esc(label)}</td><td>${t.count||0} <span style="color:var(--muted)">(${pct}%)</span></td><td>${t.fail||0}</td><td>${t.avg_ms||0} ms</td><td>${(Number(t.cost)||0).toFixed(4)} ${esc(cur)}</td></tr>`;
    }).join("");
    el.innerHTML=`<div class="ai-metric-grid">
      <div class="ai-metric"><div class="hint">调用次数</div><b>${total}</b></div>
      <div class="ai-metric"><div class="hint">失败率</div><b>${rate}%</b></div>
      <div class="ai-metric"><div class="hint">平均延迟</div><b>${avg}<span style="font-size:11px;font-weight:500;color:var(--muted)"> ms</span></b></div>
      <div class="ai-metric"><div class="hint">Token</div><b>${tok}</b></div>
      <div class="ai-metric"><div class="hint">区间 TCO</div><b>${cost.toFixed(4)}<span style="font-size:11px;font-weight:500;color:var(--muted)"> ${esc(cur)}</span></b></div>
      <div class="ai-metric"><div class="hint">日均成本</div><b>${dailyAvg.toFixed(4)}<span style="font-size:11px;font-weight:500;color:var(--muted)"> ${esc(cur)}/天</span></b></div>
      <div class="ai-metric"><div class="hint">存储</div><b style="font-size:13px">${persisted?"PostgreSQL":"进程内"}</b></div>
      <div class="ai-metric" title="采纳、点赞与差评的人工质量信号"><div class="hint">人工反馈</div><b>${fbTotal}</b></div>
      <div class="ai-metric" title="（实际应用 + 有用）/ 全部人工反馈"><div class="hint">正向反馈率</div><b>${fbPositive}%</b></div>
      <div class="ai-metric" title="真正应用到配置或输入框的结果"><div class="hint">实际采纳</div><b>${fbApplied}</b></div>
      <div class="ai-metric" title="将形成避坑经验，不直接污染已验证知识"><div class="hint">需改进</div><b>${fbUnhelpful}</b></div>
    </div>
    ${auditCard}
    ${modelCostBars?`<div class="hint">模型成本占比（TCO）</div><div style="margin-bottom:10px">${modelCostBars}</div>`:""}
    ${routeRows?`<div class="hint" title="每次调用为什么选这个模型：任务映射 / 廉价模型 / 主模型 / 故障转移">模型路由原因</div><table class="hv-mini-table" style="width:100%;margin-bottom:10px"><thead><tr><th>原因</th><th>次数</th><th>失败</th><th>均延迟</th><th>费用</th></tr></thead><tbody>${routeRows}</tbody></table>`:""}
    ${taskCostRows?`<div class="hint">任务成本 Top</div><table class="hv-mini-table" style="width:100%;margin-bottom:10px"><thead><tr><th>任务</th><th>次数</th><th>Token</th><th>费用</th></tr></thead><tbody>${taskCostRows}</tbody></table>`:""}
    <div class="chart-container ai-usage-chart" style="margin:4px 0 12px">
      <div class="hint" style="margin-bottom:6px;display:flex;align-items:center;gap:8px;flex-wrap:wrap">
        <span>历史组合曲线 · 调用 / Token / 费用</span>
        ${typeof forecastChipHTML==="function"?forecastChipHTML("ai-usage"):""}
      </div>
      <canvas id="aiUsageComboChart" height="240"></canvas>
    </div>
    ${modelBars?`<div class="hint">模型分布</div><div style="margin-bottom:10px">${modelBars}</div>`:""}
    ${users?`<div class="hint">用户成本排行</div><table class="hv-mini-table" style="width:100%;margin-bottom:10px"><thead><tr><th>用户</th><th>次数</th><th>Token</th><th>费用</th></tr></thead><tbody>${users}</tbody></table>`:""}
    ${taskRows?`<table class="hv-mini-table" style="width:100%;margin-bottom:8px"><thead><tr><th>任务</th><th>次数</th><th>失败</th><th>均延迟</th></tr></thead><tbody>${taskRows}</tbody></table>`:`<div class="hint">尚无按任务统计（完成若干 AI 调用后出现）</div>`}
    ${feedbackRows?`<div class="hint" style="margin-top:10px">人工反馈闭环 · 正向率 = 实际采纳 + 有用</div><table class="hv-mini-table" style="width:100%;margin-bottom:8px"><thead><tr><th>任务</th><th>反馈</th><th>采纳</th><th>有用</th><th>需改进</th><th>正向率</th></tr></thead><tbody>${feedbackRows}</tbody></table>`:`<div class="hint" style="margin-top:8px">尚无人工反馈；采纳/点赞/差评后将展示学习质量。</div>`}
    ${expTable}
    ${recent?`<div class="hint" style="margin-top:8px">最近调用</div>${recent}`:""}`;
    // 组合曲线：把多指标归一到同一时间轴（calls / tokens / cost）
    const pts=hist.points||[];
    const samples=pts.map(p=>({
      timestamp: p.timestamp,
      calls: p.calls||0,
      tokens: p.tokens||0,
      cost: p.cost||0,
      avg_latency_ms: p.avg_latency_ms||0,
    }));
    const aiSer=[
      { key:"calls", label:"调用次数", color:"#4c8dff", fmt:v=>v.toFixed(0) },
      { key:"tokens", label:"Token", color:"#22c55e", fmt:v=>v.toFixed(0) },
      { key:"cost", label:`费用(${cur})`, color:"#f97316", fmt:v=>v.toFixed(4) },
    ];
    // 外层 hint 已标明「历史组合曲线」，画布不再重复标题
    if(!load.isCurrent()) return;
    if(typeof createChartWithForecast==="function" && typeof isChartForecastOn==="function" && isChartForecastOn("ai-usage")){
      await createChartWithForecast("aiUsageComboChart", samples, aiSer, 0, null, {
        title:"", noEntrance:true, cssH:240, legendMode:"dash", forecast:true, forecastScope:"ai-usage",
        signal:load.signal, isCurrent:load.isCurrent
      });
    } else if(typeof createChart==="function"){
      createChart("aiUsageComboChart", samples, aiSer, 0, null, { title:"", noEntrance:true, cssH:240, legendMode:"dash" });
    }
  }catch(e){
    if(e && (e.name==="AbortError" || /aborted/i.test(String(e.message||e)))) return;
    if(!load.isCurrent()) return;
    el.innerHTML=`<div class="hint">${I18N.t("sre.load_failed","加载失败")}：${esc(String(e))}</div>`;
  }
}
document.addEventListener("chart-forecast-toggle",(ev)=>{
  if(!ev.detail) return;
  if(ev.detail.scope==="ai-usage") loadAIStats();
  if(ev.detail.scope==="slo-trend") loadSloTrend();
  if(ev.detail.scope===AI_CHAT_FC_SCOPE) rebindAIChatChartsForecast();
});

// 值班晨报：拉取服务端态势汇总（未决事件/SLO/待审批修复/巡检）→ 走统一 /ai/assist 流式生成
async function genDutyReport(){
  let j;
  try { j = await fetch(`${API}/ai/duty-context`).then(r=>r.json()); }
  catch(e){ toast(I18N.t("sre.duty_ctx_failed","获取运维态势失败")+"："+e,"err"); return; }
  openAIAssist({
    task:"duty_report",
    title:"🌅 "+I18N.t("sre.duty_report_title","AI 值班晨报"),
    mode:"analyze",
    context:(j&&j.context)?j.context:"（当前无态势数据）",
    hint:(j&&j.notable===false)?I18N.t("sre.duty_calm","当前态势平静，无未决事件/SLO超标/待审批修复。"):I18N.t("sre.duty_summarizing","正在汇总今日运维态势…")
  });
}
function applyAIConfigEditMode(editable){
  const root=$("aiConfigMask"); if(!root) return;
  root.querySelectorAll("input, textarea, select").forEach(el=>{ el.disabled=!editable; });
  root.querySelectorAll("button").forEach(el=>{
    if(el.hasAttribute("data-close-btn") || el.classList.contains("close") || el.classList.contains("ai-nav-item")) return;
    const id=el.id||"";
    if(/^(mcpCopyClientCfgBtn|mcpRefreshClientCfgBtn|mcpClientAddBtn|mcpClientTestBtn|mcpClientSyncBtn|mcpClientSaveBtn|mcpClientCancelBtn)$/.test(id)){ el.disabled=false; return; }
    const isWrite=el.getAttribute("data-act")==="ai-preset"
      || /^(aiConfigSaveBtn|aiConfigSaveCloseBtn|aiChatTestBtn|aiEmbedTestBtn|aiRerankTestBtn|aiWeKnoraTestBtn|aiWeKnoraListBtn|aiModelRefreshBtn|aiModelCaretBtn|mcpGenTokenBtn|aiTermToggleBtn|aiTermConfirmBtn|aiTermCancelBtn|aiStatsRefreshBtn|aiJumpObserveAbBtn|aiJumpQualityRouteBtn)$/.test(id)
      || id.indexOf("Test")>=0 || id.indexOf("Gen")>=0;
    if(isWrite) el.disabled=!editable;
  });
  const save=$("aiConfigSaveBtn"); if(save){ save.disabled=!editable; save.style.display=editable?"":"none"; }
  const saveClose=$("aiConfigSaveCloseBtn"); if(saveClose){ saveClose.disabled=!editable; saveClose.style.display=editable?"":"none"; }
  const foot=root.querySelector(".ai-settings-foot-hint");
  if(foot){
    if(editable){
      foot.innerHTML='<span class="ai-foot-chip">'+(I18N.t("sre.ai_save_need_chip","配置项需保存")||"配置项需保存")+'</span>'
        +'<span class="ai-foot-chip muted">'+(I18N.t("sre.ai_save_instant_chip","终端授权 / 观测刷新即时生效")||"终端授权 / 观测刷新即时生效")+'</span>';
    } else {
      foot.innerHTML='<span class="ai-foot-chip muted">'+(I18N.t("sre.ai_settings_readonly","当前为只读查看；修改 Provider / 密钥需管理员。")||"当前为只读查看")+'</span>';
    }
  }
}

async function openAIConfig(){
  const editable=!(typeof isAdmin==="function") || isAdmin();
  const tr=$("aiChatTestResult"); if(tr){ tr.textContent=""; tr.className="ai-test-result"; }
  const er=$("aiEmbedTestResult"); if(er){ er.textContent=""; er.className="ai-test-result"; }
  const wr=$("aiWeKnoraTestResult"); if(wr){ wr.textContent=""; wr.className="ai-test-result"; }
  const sr=$("aiSpeechTestResult"); if(sr){ sr.textContent=""; sr.className="ai-test-result"; }
  try { const c=await fetch(`${API}/ai/config`).then(r=>r.json());
    $("aiEnabled").checked=!!c.enabled; $("aiEndpoint").value=c.endpoint||""; $("aiKey").value=c.api_key||""; $("aiModel").value=c.model||""; $("aiInterval").value=c.inspect_interval_min||30;
    $("embedEndpoint").value=c.embed_endpoint||""; $("embedKey").value=c.embed_api_key||""; $("embedModel").value=c.embed_model||""; $("embedDim").value=c.embed_dimensions||"";
    if($("rerankEndpoint")){ $("rerankEndpoint").value=c.rerank_endpoint||""; $("rerankKey").value=c.rerank_api_key||""; $("rerankModel").value=c.rerank_model||""; }
    if($("aiSelfVerify")) $("aiSelfVerify").checked=!!c.self_verify;
    if($("aiMoAModels")) $("aiMoAModels").value=c.moa_models||"";
    if($("aiInputPrice")) $("aiInputPrice").value=c.input_price_per_1m||"";
    if($("aiOutputPrice")) $("aiOutputPrice").value=c.output_price_per_1m||"";
    if($("aiCostCurrency")) $("aiCostCurrency").value=c.cost_currency||"CNY";
    if($("aiCheapModel")) $("aiCheapModel").value=c.cheap_model||"";
    if($("aiTaskModels")) $("aiTaskModels").value=c.task_models_json||"";
    if($("aiActiveExperiment")) $("aiActiveExperiment").value=c.active_experiment_id||"";
    if($("mcpEnabled")) $("mcpEnabled").checked=!!c.mcp_enabled;
    if($("mcpToken")) $("mcpToken").value=c.mcp_token||"";
    if($("mcpRateLimit")) $("mcpRateLimit").value=c.mcp_rate_limit_per_min||"";
    if($("mcpScopedTokens")) $("mcpScopedTokens").value=c.mcp_scoped_tokens_json||"";
    loadMCPClientsFromJSON(c.mcp_clients_json||"");
    refreshMcpClientConfig();
    updateMcpCardSummary();
    if($("weknoraEnabled")) $("weknoraEnabled").checked=!!c.weknora_enabled;
    if($("weknoraURL")) $("weknoraURL").value=c.weknora_url||"";
    if($("weknoraKey")) $("weknoraKey").value=c.weknora_api_key||"";
    if($("weknoraKBIDs")) $("weknoraKBIDs").value=c.weknora_knowledge_base_ids||"";
    if($("disablePublicChatMemory")) $("disablePublicChatMemory").checked=!!c.disable_public_chat_memory;
    if($("allowUnverifiedAIOutputLearning")) $("allowUnverifiedAIOutputLearning").checked=!!c.allow_unverified_ai_output_learning;
    if($("autoDefendEnabled")) $("autoDefendEnabled").checked=!!c.auto_defend_enabled;
    if($("selfEvolveEnabled")) $("selfEvolveEnabled").checked=!!c.self_evolve_enabled;
    if($("aiDailyQuota")) $("aiDailyQuota").value=c.daily_quota_per_user||0;
    if($("aiWriteToolsApproval")) $("aiWriteToolsApproval").checked=c.write_tools_require_approval!==false;
    if($("aiRedactSensitive")) $("aiRedactSensitive").checked=!!c.redact_sensitive_fields;
    if($("speechPreferCloud")) $("speechPreferCloud").checked=!!c.speech_prefer_cloud;
    if($("speechEndpoint")) $("speechEndpoint").value=c.speech_endpoint||"";
    if($("speechKey")) $("speechKey").value=c.speech_api_key||"";
    if($("speechSTTModel")) $("speechSTTModel").value=c.speech_stt_model||"";
    if($("speechTTSModel")) $("speechTTSModel").value=c.speech_tts_model||"";
    if($("speechTTSVoice")) $("speechTTSVoice").value=c.speech_tts_voice||"";
    AI_SPEECH_STATUS={prefer:!!c.speech_prefer_cloud,stt:!!(c.speech_stt_model||"").trim(),tts:!!(c.speech_tts_model||"").trim()};
    AI_TERM_ENABLED=!!(c.ai_terminal_enabled ?? c.hermes_terminal_enabled); renderAITermState();
    updateAllAISettingsSummaries();
    applyAiCardCollapsedState();
    syncAIPresetActive();
    markAIConfigClean();
    bindAIConfigDirtyTracking();
    switchAISettingsTab("provider");
    loadAIStats();
    loadABExperiments();
  } catch(e){}
  if(editable) loadAIModels();
  applyAIConfigEditMode(editable);
  $("aiConfigMask").classList.add("show");
  if(!editable && typeof toast==="function"){
    toast(I18N.t("sre.ai_settings_readonly","当前为只读查看；修改 Provider / 密钥需管理员。"),"ok");
  }
}

function switchAISettingsTab(tab){
  const name=tab||"provider";
  document.querySelectorAll(".ai-nav-item").forEach(b=>{
    b.classList.toggle("active", b.getAttribute("data-ai-tab")===name);
  });
  document.querySelectorAll(".ai-panel").forEach(p=>{
    p.classList.toggle("active", p.getAttribute("data-ai-panel")===name);
  });
  if(name==="observe"){ loadAIStats(); loadABExperiments(); }
}
// ===== AI 终端只读巡检权限：独立开关，开启需终端连接密码 =====
let AI_TERM_ENABLED=false;
function renderAITermState(){
  const lbl=$("aiTermStateLabel"), btn=$("aiTermToggleBtn"), row=$("aiTermPwRow"), msg=$("aiTermMsg");
  if(lbl){ lbl.textContent=AI_TERM_ENABLED?I18N.t("sre.term_on","已开启"):I18N.t("sre.term_off","未开启"); lbl.className="ai-term-state "+(AI_TERM_ENABLED?"on":"off"); }
  if(btn){ btn.textContent=AI_TERM_ENABLED?I18N.t("sre.term_disable","关闭"):I18N.t("sre.term_enable","开启"); }
  if(row) row.style.display="none";
  if(msg){ msg.textContent=""; msg.className="ai-term-msg"; }
  if(typeof updateSecurityPanelSummary==="function") updateSecurityPanelSummary();
  if(typeof updateAISettingsNavDots==="function") updateAISettingsNavDots();
}
function toggleAITerm(){
  if(AI_TERM_ENABLED){ aiTermSet(false,""); return; } // 关闭无需密码
  const row=$("aiTermPwRow"); if(row) row.style.display="flex";
  const pw=$("aiTermPw"); if(pw){ pw.value=""; setTimeout(()=>pw.focus(),50); }
}
function confirmAITerm(){
  const pw=$("aiTermPw"), msg=$("aiTermMsg"), password=pw?pw.value:"";
  if(!password){ if(msg){ msg.textContent=I18N.t("sre.term_need_pw","请输入终端连接密码"); msg.className="ai-term-msg err"; } return; }
  aiTermSet(true,password);
}
async function aiTermSet(enabled,password){
  const msg=$("aiTermMsg");
  try{
    const r=await fetch(`${API}/ai/terminal-access`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({enabled,password})});
    const j=await r.json().catch(()=>({}));
    if(!r.ok){ if(msg){ msg.textContent="✗ "+(j.error||("HTTP "+r.status)); msg.className="ai-term-msg err"; } return; }
    AI_TERM_ENABLED=!!j.enabled; renderAITermState();
    if(msg){ msg.textContent=AI_TERM_ENABLED?"✓ "+I18N.t("sre.term_enabled_msg","已开启：AI 可执行只读终端巡检（仅查询，禁止任何增删改）"):I18N.t("sre.term_disabled_msg","已关闭 AI 终端巡检"); msg.className="ai-term-msg ok"; }
    if(typeof toast==="function") toast(AI_TERM_ENABLED?I18N.t("sre.term_toast_on","已开启 AI 终端只读巡检"):I18N.t("sre.term_disabled_msg","已关闭 AI 终端巡检"),"ok");
  }catch(e){ if(msg){ msg.textContent="✗ "+I18N.t("sre.request_failed","请求失败")+"："+e; msg.className="ai-term-msg err"; } }
}
// 从当前表单 Endpoint+Key 自动获取该 Provider 的可用模型，填充自定义下拉（可搜索）；
// 获取不到时保留手动输入。不再内置任何预设模型。
let _aiModelsReq=0;
let AI_MODELS=[]; // 已获取的可选模型 [{value,label}]
async function loadAIModels(){
  const info=$("aiModelInfo");
  const ep=($("aiEndpoint").value||"").trim();
  const myReq=++_aiModelsReq;
  if(!ep){ AI_MODELS=[]; renderModelDropdown(); if(info) info.textContent="· "+I18N.t("sre.model_hint_empty","填入 Endpoint 后自动获取，或直接手动输入模型名"); return; }
  if(info) info.textContent="· "+I18N.t("sre.model_fetching","获取中…");
  try {
    const body={endpoint:ep,api_key:$("aiKey").value||""};
    const m=await fetch(`${API}/ai/models`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)}).then(r=>r.json());
    if(myReq!==_aiModelsReq) return; // 有更新的请求在途，丢弃过期结果
    AI_MODELS=(m&&Array.isArray(m.models))?m.models:[];
    renderModelDropdown();
    if(info) info.textContent=AI_MODELS.length
      ? `· ${I18N.t("sre.model_got_prefix","已获取")} ${AI_MODELS.length} ${I18N.t("sre.model_got_suffix","个模型，点输入框展开选择 / 搜索 / 手动输入")}`
      : "· "+I18N.t("sre.model_none","未获取到模型，请检查 Endpoint/Key，或直接手动输入模型名");
  } catch(e){ if(myReq!==_aiModelsReq) return; if(info) info.textContent="· "+I18N.t("sre.model_fetch_failed","获取失败，可手动输入模型名"); }
}
// 自定义模型下拉：始终显示全部已获取模型（可按输入内容过滤），点选填入输入框。
// 替代原生 <datalist>——原生下拉会按输入框【已有值】过滤，导致“提示 N 个却只显示 1 个”。
function renderModelDropdown(filter){
  const dd=$("aiModelDropdown"); if(!dd) return;
  const f=(filter||"").trim().toLowerCase();
  const list=AI_MODELS.filter(x=>!f || String(x.value).toLowerCase().includes(f) || String(x.label||"").toLowerCase().includes(f));
  if(!list.length){ dd.innerHTML=`<div class="ai-model-empty">${AI_MODELS.length?I18N.t("sre.model_no_match","无匹配模型"):I18N.t("sre.model_empty","暂无模型，填好 Endpoint+Key 后点刷新")}</div>`; return; }
  dd.innerHTML=list.map(x=>`<div class="ai-model-opt" data-val="${esc(x.value)}" title="${esc(x.value)}">${esc(x.label||x.value)}</div>`).join("");
  dd.querySelectorAll(".ai-model-opt").forEach(el=>el.onclick=()=>{ const t=$("aiModel"); if(t) t.value=el.dataset.val; hideModelDropdown(); });
}
function showModelDropdown(){ const dd=$("aiModelDropdown"); if(!dd) return; renderModelDropdown(); dd.style.display="block"; } // 展开显示全部（不按已选值过滤，正是修复点）
function hideModelDropdown(){ const dd=$("aiModelDropdown"); if(dd) dd.style.display="none"; }
function toggleModelDropdown(){ const dd=$("aiModelDropdown"); if(dd&&dd.style.display==="block") hideModelDropdown(); else showModelDropdown(); }
// AI 预设:仅设置 Endpoint（两种接口类型:OpenAI 兼容 / Anthropic，按端点自动识别）。
// 取消默认预设模型：切换 Provider 后清空模型，改由自动获取 / 搜索 / 手动输入。
function setAIPreset(type){
  const presets={
    "bailian":{endpoint:"https://dashscope.aliyuncs.com/compatible-mode/v1",label:I18N.t("sre.preset_bailian","阿里云百炼（OpenAI 兼容）")},
    "openai":{endpoint:"https://api.openai.com/v1",label:"OpenAI"},
    "deepseek":{endpoint:"https://api.deepseek.com/v1",label:"DeepSeek"},
    "ollama":{endpoint:"http://localhost:11434/v1",label:I18N.t("sre.preset_ollama","本地 Ollama")},
    "claude":{endpoint:"https://dashscope.aliyuncs.com/apps/anthropic",label:I18N.t("sre.preset_claude","Claude（百炼 Anthropic）")},
  };
  const p=presets[type]; if(!p) return;
  $("aiEndpoint").value=p.endpoint;
  $("aiModel").value=""; // 取消默认预设模型，切 Provider 后需重新获取/输入
  syncAIPresetActive();
  markAIConfigDirty();
  toast(`${I18N.t("sre.preset_set","已设为")} ${p.label} · ${I18N.t("sre.fetching_models","正在获取模型…")}`,"ok");
  loadAIModels(); // 选预设后自动获取该 provider 的模型
}
async function saveAIConfig(opts){
  if(typeof isAdmin==="function" && !isAdmin()){ toast(I18N.t("toast.admin_only","仅管理员可操作"),"err"); return; }
  const enabled=$("aiEnabled").checked, endpoint=$("aiEndpoint").value.trim(), model=$("aiModel").value.trim();
  if(enabled && (!endpoint || !model)){ toast(I18N.t("sre.ai_need_endpoint_model","启用 AI 需填写 Endpoint 和模型"),"err"); return; } // 轻校验：启用却没填必填项
  const body={enabled,endpoint,api_key:$("aiKey").value,model,inspect_interval_min:parseInt($("aiInterval").value)||30,
    embed_endpoint:$("embedEndpoint").value.trim(),embed_api_key:$("embedKey").value,embed_model:$("embedModel").value.trim(),embed_dimensions:parseInt($("embedDim").value)||0,
    rerank_endpoint:($("rerankEndpoint")?.value||"").trim(),rerank_api_key:$("rerankKey")?.value||"",rerank_model:($("rerankModel")?.value||"").trim(),
    self_verify:$("aiSelfVerify")?.checked||false,moa_models:($("aiMoAModels")?.value||"").trim(),
    input_price_per_1m:parseFloat($("aiInputPrice")?.value)||0,
    output_price_per_1m:parseFloat($("aiOutputPrice")?.value)||0,
    cost_currency:($("aiCostCurrency")?.value||"CNY").trim()||"CNY",
    cheap_model:($("aiCheapModel")?.value||"").trim(),
    task_models_json:($("aiTaskModels")?.value||"").trim(),
    active_experiment_id:($("aiActiveExperiment")?.value||"").trim(),
    mcp_enabled:$("mcpEnabled")?.checked||false,mcp_token:($("mcpToken")?.value||"").trim(),
    mcp_rate_limit_per_min:parseInt($("mcpRateLimit")?.value,10)||0,
    mcp_scoped_tokens_json:($("mcpScopedTokens")?.value||"").trim(),
    mcp_clients_json: serializeMCPClientsJSON(),
    weknora_enabled:$("weknoraEnabled")?.checked||false,
    weknora_url:($("weknoraURL")?.value||"").trim(),
    weknora_api_key:$("weknoraKey")?.value||"",
    weknora_knowledge_base_ids:($("weknoraKBIDs")?.value||"").trim(),
    disable_public_chat_memory:$("disablePublicChatMemory")?.checked||false,
    allow_unverified_ai_output_learning:$("allowUnverifiedAIOutputLearning")?.checked||false,
    auto_defend_enabled:$("autoDefendEnabled")?.checked||false,
    self_evolve_enabled:$("selfEvolveEnabled")?.checked||false,
    daily_quota_per_user:$("aiDailyQuota")? (parseInt($("aiDailyQuota").value,10)||0) : 0,
    write_tools_require_approval:$("aiWriteToolsApproval")? !!$("aiWriteToolsApproval").checked : false,
    redact_sensitive_fields:$("aiRedactSensitive")? !!$("aiRedactSensitive").checked : false,
    speech_prefer_cloud:$("speechPreferCloud")? !!$("speechPreferCloud").checked : false,
    speech_endpoint:($("speechEndpoint")?.value||"").trim(),
    speech_api_key:$("speechKey")?.value||"",
    speech_stt_model:($("speechSTTModel")?.value||"").trim(),
    speech_tts_model:($("speechTTSModel")?.value||"").trim(),
    speech_tts_voice:($("speechTTSVoice")?.value||"").trim()};
  const r=await fetch(`${API}/ai/config`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
  if(r.ok){
    AI_SPEECH_STATUS={prefer:!!body.speech_prefer_cloud,stt:!!body.speech_stt_model,tts:!!body.speech_tts_model};
    markAIConfigClean();
    updateAllAISettingsSummaries();
    toast(I18N.t("toast.saved","已保存"),"ok");
    if(opts && opts.close){ $("aiConfigMask").classList.remove("show"); }
  } else toast(I18N.t("toast.save_failed","保存失败"),"err");
}
async function saveAIConfigAndClose(){ return saveAIConfig({close:true}); }
// AI 对话模型连接测试：通过 SSE 流式验证 Provider 连通性，展示延迟 + 回复摘要
let _aiTestBusy=false;
async function testAIChatConfig(){
  if(_aiTestBusy) return;
  const el=$("aiChatTestResult");
  const endpoint=$("aiEndpoint").value.trim(), model=$("aiModel").value.trim();
  if(!endpoint||!model){ if(el){ el.textContent="✗ "+I18N.t("sre.fill_endpoint_model","请先填写 Endpoint 和模型"); el.className="ai-test-result err"; } return; }
  _aiTestBusy=true;
  const testBtn=$("aiChatTestBtn"); if(testBtn) testBtn.disabled=true;
  if(el){ el.textContent=I18N.t("sre.ai_chat_model","对话模型")+" "+I18N.t("sre.testing","测试中…"); el.className="ai-test-result testing"; }
  const body={enabled:true,endpoint,api_key:$("aiKey").value,model};
  try{
    const r=await fetch(`${API}/ai/test`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    if(!r.ok){ throw new Error("HTTP "+r.status); }
    let resultMeta=null, reply="", error=null;
    await readSSEStream(r,
      (delta,fullText)=>{ reply=fullText; },
      (err)=>{ error=err; },
      (fullText)=>{ if(!reply) reply=fullText; },
      (meta)=>{ resultMeta=meta; }
    );
    if(!el) return;
    if(error){
      el.textContent="✗ "+I18N.t("sre.ai_chat_model","对话模型")+" "+error; el.className="ai-test-result err"; el.style.whiteSpace="pre-wrap";
      return;
    }
    if(resultMeta && resultMeta.ok){
      let extra="";
      if(resultMeta.provider_hint){
        const labels={openai:I18N.t("sre.compat_openai","OpenAI 兼容"),"bailian-compat":I18N.t("sre.compat_bailian","百炼兼容"),anthropic:"Anthropic"};
        extra=` · ${labels[resultMeta.provider_hint]||resultMeta.provider_hint}`;
      }
      el.textContent=`✓ ${I18N.t("sre.chat_model_ok","对话模型可用")}${extra} · ${resultMeta.latency_ms||0}ms · ${(resultMeta.reply||"").slice(0,48)}`; el.className="ai-test-result ok";
    } else if(reply){
      el.textContent=`✓ ${I18N.t("sre.chat_model_ok","对话模型可用")} · ${reply.slice(0,48)}`; el.className="ai-test-result ok";
    } else {
      el.textContent="✗ "+I18N.t("sre.ai_chat_model","对话模型")+" "+I18N.t("sre.no_valid_reply","未收到有效回复"); el.className="ai-test-result err";
    }
  }catch(e){ if(el){ el.textContent="✗ "+I18N.t("sre.ai_chat_model","对话模型")+" "+I18N.t("sre.request_failed","请求失败")+"："+e; el.className="ai-test-result err"; } }
  finally{ _aiTestBusy=false; if(testBtn) testBtn.disabled=false; }
}

// AI 语音配置测试：TTS 合成样例并播放；若配置了 STT 则对同段音频做识别闭环
let _aiSpeechTestBusy=false, _aiSpeechTestAudio=null;
function stopAISpeechTestAudio(){
  if(_aiSpeechTestAudio){
    try{ _aiSpeechTestAudio.pause(); }catch(e){}
    try{ if(_aiSpeechTestAudio._objectUrl) URL.revokeObjectURL(_aiSpeechTestAudio._objectUrl); }catch(e){}
    _aiSpeechTestAudio=null;
  }
}
function playAISpeechTestAudio(b64, contentType){
  stopAISpeechTestAudio();
  if(!b64) return Promise.reject(new Error("无音频数据"));
  const bin=atob(b64);
  const bytes=new Uint8Array(bin.length);
  for(let i=0;i<bin.length;i++) bytes[i]=bin.charCodeAt(i);
  const blob=new Blob([bytes],{type:contentType||"audio/mpeg"});
  const url=URL.createObjectURL(blob);
  const audio=new Audio(url);
  audio._objectUrl=url;
  _aiSpeechTestAudio=audio;
  const cleanup=()=>{
    try{URL.revokeObjectURL(url);}catch(e){}
    if(_aiSpeechTestAudio===audio) _aiSpeechTestAudio=null;
  };
  audio.onended=cleanup;
  audio.onerror=()=>{ cleanup(); };
  return audio.play();
}
async function testAISpeechConfig(){
  if(_aiSpeechTestBusy) return;
  const el=$("aiSpeechTestResult");
  const ttsModel=($("speechTTSModel")?.value||"").trim();
  if(!ttsModel){
    if(el){ el.textContent="✗ 请先填写 TTS 播报模型"; el.className="ai-test-result err"; }
    return;
  }
  _aiSpeechTestBusy=true;
  stopAISpeechTestAudio();
  const testBtn=$("aiSpeechTestBtn"); if(testBtn) testBtn.disabled=true;
  if(el){ el.textContent="语音 "+I18N.t("sre.testing","测试中…"); el.className="ai-test-result testing"; }
  const body={
    enabled:true,
    endpoint:($("aiEndpoint")?.value||"").trim(),
    api_key:$("aiKey")?.value||"",
    speech_prefer_cloud:$("speechPreferCloud")? !!$("speechPreferCloud").checked : false,
    speech_endpoint:($("speechEndpoint")?.value||"").trim(),
    speech_api_key:$("speechKey")?.value||"",
    speech_stt_model:($("speechSTTModel")?.value||"").trim(),
    speech_tts_model:ttsModel,
    speech_tts_voice:($("speechTTSVoice")?.value||"").trim()
  };
  try{
    const r=await fetch(`${API}/ai/test-speech`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    const j=await r.json().catch(()=>({}));
    if(!el) return;
    if(!j.ok){
      el.textContent="✗ "+(j.error||I18N.t("sre.test_failed","测试失败"));
      el.className="ai-test-result err";
      el.style.whiteSpace="pre-wrap";
      return;
    }
    let playErr="";
    if(j.audio_base64){
      try{ await playAISpeechTestAudio(j.audio_base64, j.content_type); }
      catch(e){ playErr=String(e&&e.message?e.message:e); }
    } else {
      playErr="未返回音频";
    }
    const parts=[`✓ TTS 可用 · ${j.tts_latency_ms||j.latency_ms||0}ms · ${j.model||ttsModel}`];
    if(j.voice) parts.push(j.voice);
    if(j.stt_ok){
      const tr=(j.transcript||"").trim().slice(0,36);
      parts.push(`STT ${j.stt_latency_ms||0}ms${tr?" · "+tr:""}`);
    } else if(j.stt_error){
      parts.push("STT 未通过");
    } else if(j.stt_skipped){
      parts.push("未测 STT");
    }
    if(playErr){
      el.textContent=parts.join(" · ")+" · 播放失败："+playErr;
      el.className="ai-test-result err";
      el.style.whiteSpace="pre-wrap";
    } else {
      el.textContent=parts.join(" · ")+" · 已播报";
      el.className="ai-test-result ok";
      el.style.whiteSpace="nowrap";
      if(j.stt_error && typeof toast==="function"){
        toast("TTS 已通过，STT 回环失败："+j.stt_error,"warn");
      }
    }
  }catch(e){
    if(el){ el.textContent="✗ 语音 "+I18N.t("sre.request_failed","请求失败")+"："+e; el.className="ai-test-result err"; }
  }finally{
    _aiSpeechTestBusy=false; if(testBtn) testBtn.disabled=false;
  }
}

// AI 向量化模型连接测试
let _aiEmbedTestBusy=false;
async function testAIEmbedConfig(){
  if(_aiEmbedTestBusy) return;
  const el=$("aiEmbedTestResult");
  _aiEmbedTestBusy=true;
  const testBtn=$("aiEmbedTestBtn"); if(testBtn) testBtn.disabled=true;
  if(el){ el.textContent=I18N.t("sre.ai_embed_model","向量化模型")+" "+I18N.t("sre.testing","测试中…"); el.className="ai-test-result testing"; }
  const body={enabled:true,
    embed_endpoint:$("embedEndpoint").value.trim(),
    embed_api_key:$("embedKey").value,
    embed_model:$("embedModel").value.trim(),
    embed_dimensions:parseInt($("embedDim").value)||0,
    endpoint:$("aiEndpoint").value.trim(),
    api_key:$("aiKey").value
  };
  try{
    const r=await fetch(`${API}/ai/test-embed`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    const j=await r.json().catch(()=>({}));
    if(!el) return;
    if(j.ok){
      el.textContent=`✓ ${I18N.t("sre.embed_model_ok","向量化模型可用")} · ${j.latency_ms||0}ms · ${j.dimensions||0}${I18N.t("sre.dim_unit","维")} · ${j.model||""}`; el.className="ai-test-result ok";
    } else {
      el.textContent="✗ "+I18N.t("sre.ai_embed_model","向量化模型")+" "+(j.error||I18N.t("sre.test_failed","测试失败")); el.className="ai-test-result err";
    }
  }catch(e){ if(el){ el.textContent="✗ "+I18N.t("sre.ai_embed_model","向量化模型")+" "+I18N.t("sre.request_failed","请求失败")+"："+e; el.className="ai-test-result err"; } }
  finally{ _aiEmbedTestBusy=false; if(testBtn) testBtn.disabled=false; }
}

// ===== AI 设置：统一折叠卡片 + 摘要 / 脏状态 =====
const AI_CARD_DEFS = {
  providerChat: { body:"providerChatCardBody", arrow:"providerChatCardArrow", header:"providerChatCardHeader", card:"providerChatCard", storage:"aiops_ai_card_providerChat", defaultCollapsed:false },
  providerSpeech: { body:"providerSpeechCardBody", arrow:"providerSpeechCardArrow", header:"providerSpeechCardHeader", card:"providerSpeechCard", storage:"aiops_ai_card_providerSpeech", defaultCollapsed:true },
  embed: { body:"embedCardBody", arrow:"embedCardArrow", header:"embedCardHeader", card:"embedCard", storage:"aiops_ai_card_embed", defaultCollapsed:false },
  rerank: { body:"rerankCardBody", arrow:"rerankCardArrow", header:"rerankCardHeader", card:"rerankCard", storage:"aiops_ai_card_rerank", defaultCollapsed:true },
  weknora: { body:"weknoraCardBody", arrow:"weknoraCardArrow", header:"weknoraCardHeader", card:"weknoraCard", storage:"aiops_ai_card_weknora", defaultCollapsed:true },
  qualityDiag: { body:"qualityDiagCardBody", arrow:"qualityDiagCardArrow", header:"qualityDiagCardHeader", card:"qualityDiagCard", storage:"aiops_ai_card_qualityDiag", defaultCollapsed:false },
  qualityCost: { body:"qualityCostCardBody", arrow:"qualityCostCardArrow", header:"qualityCostCardHeader", card:"qualityCostCard", storage:"aiops_ai_card_qualityCost", defaultCollapsed:false },
  qualityRoute: { body:"qualityRouteCardBody", arrow:"qualityRouteCardArrow", header:"qualityRouteCardHeader", card:"qualityRouteCard", storage:"aiops_ai_card_qualityRoute", defaultCollapsed:true },
  mcpServer: { body:"mcpServerCardBody", arrow:"mcpServerCardArrow", header:"mcpServerCardHeader", card:"mcpServerCard", storage:"aiops_mcp_server_collapsed", defaultCollapsed:false },
  mcpClients: { body:"mcpClientsCardBody", arrow:"mcpClientsCardArrow", header:"mcpClientsCardHeader", card:"mcpClientsCard", storage:"aiops_mcp_clients_collapsed", defaultCollapsed:false },
  securityQuota: { body:"securityQuotaCardBody", arrow:"securityQuotaCardArrow", header:"securityQuotaCardHeader", card:"securityQuotaCard", storage:"aiops_ai_card_securityQuota", defaultCollapsed:false },
  securityMemory: { body:"securityMemoryCardBody", arrow:"securityMemoryCardArrow", header:"securityMemoryCardHeader", card:"securityMemoryCard", storage:"aiops_ai_card_securityMemory", defaultCollapsed:false },
  securityTerm: { body:"securityTermCardBody", arrow:"securityTermCardArrow", header:"securityTermCardHeader", card:"securityTermCard", storage:"aiops_ai_card_securityTerm", defaultCollapsed:false }
};
function aiCardStoredCollapsed(key){
  const def=AI_CARD_DEFS[key]; if(!def) return true;
  try{
    const raw=localStorage.getItem(def.storage);
    if(raw==null) return !!def.defaultCollapsed;
    return raw==="1";
  }catch(_){ return !!def.defaultCollapsed; }
}
function setAiCardCollapsed(key, collapsed){
  const def=AI_CARD_DEFS[key]; if(!def) return;
  try{ localStorage.setItem(def.storage, collapsed ? "1" : "0"); }catch(_){}
  const body=$(def.body), arrow=$(def.arrow), header=$(def.header), card=$(def.card);
  if(body) body.style.display = collapsed ? "none" : "";
  if(arrow) arrow.classList.toggle("open", !collapsed);
  if(header) header.setAttribute("aria-expanded", collapsed ? "false" : "true");
  if(card) card.classList.toggle("is-collapsed", !!collapsed);
}
function toggleAiCard(key, ev){
  if(ev && ev.target && ev.target.closest && ev.target.closest("input,button,a,select,textarea,label")) return;
  setAiCardCollapsed(key, !aiCardStoredCollapsed(key));
}
function applyAiCardCollapsedState(){
  const forceOpen=[];
  if(($("rerankModel")?.value||"").trim()) forceOpen.push("rerank");
  if($("weknoraEnabled")?.checked) forceOpen.push("weknora");
  if(($("speechSTTModel")?.value||"").trim() || ($("speechTTSModel")?.value||"").trim() || $("speechPreferCloud")?.checked) forceOpen.push("providerSpeech");
  if(($("aiCheapModel")?.value||"").trim() || ($("aiTaskModels")?.value||"").trim() || ($("aiActiveExperiment")?.value||"").trim()) forceOpen.push("qualityRoute");
  Object.keys(AI_CARD_DEFS).forEach(key=>{
    let collapsed=aiCardStoredCollapsed(key);
    if(forceOpen.includes(key)) collapsed=false;
    setAiCardCollapsed(key, collapsed);
  });
}
function toggleEmbedCard(ev){ toggleAiCard("embed", ev); }
function toggleRerankCard(ev){ toggleAiCard("rerank", ev); }
function toggleMcpCard(){ /* legacy */ }
function setMcpSectionCollapsed(kind, collapsed){ setAiCardCollapsed(kind==="server"?"mcpServer":"mcpClients", collapsed); }
function applyMcpSectionCollapsed(){ applyAiCardCollapsedState(); }
function toggleMcpServerCard(ev){ toggleAiCard("mcpServer", ev); }
function toggleMcpClientsCard(ev){ toggleAiCard("mcpClients", ev); }

let _aiConfigDirty=false;
function markAIConfigDirty(){
  _aiConfigDirty=true;
  const foot=$("aiConfigMask") && $("aiConfigMask").querySelector(".ai-settings-foot");
  if(foot) foot.classList.add("is-dirty");
}
function markAIConfigClean(){
  _aiConfigDirty=false;
  const foot=$("aiConfigMask") && $("aiConfigMask").querySelector(".ai-settings-foot");
  if(foot) foot.classList.remove("is-dirty");
}
function bindAIConfigDirtyTracking(){
  const root=$("aiConfigMask"); if(!root || root._aiDirtyBound) return;
  root._aiDirtyBound=true;
  root.addEventListener("input", e=>{
    if(!e.target || !e.target.closest) return;
    if(e.target.closest("input,textarea,select")) markAIConfigDirty();
  });
  root.addEventListener("change", e=>{
    if(!e.target || !e.target.closest) return;
    if(e.target.closest("input,textarea,select")){
      markAIConfigDirty();
      updateAllAISettingsSummaries();
      syncAIPresetActive();
    }
  });
}
function confirmCloseAIConfigIfDirty(){
  if(!_aiConfigDirty) return true;
  return confirm(I18N.t("sre.ai_dirty_leave","有未保存的更改，确定关闭？")||"有未保存的更改，确定关闭？");
}
function tryCloseAIConfigMask(){
  if(!confirmCloseAIConfigIfDirty()) return false;
  markAIConfigClean();
  const m=$("aiConfigMask"); if(m) m.classList.remove("show");
  return true;
}
(function wrapCloseMaskForAIConfig(){
  if(typeof closeMask!=="function" || closeMask._aiWrapped) return;
  const orig=closeMask;
  window.closeMask=function(mask){
    if(mask && mask.id==="aiConfigMask"){
      if(!confirmCloseAIConfigIfDirty()) return;
      markAIConfigClean();
    }
    return orig(mask);
  };
  window.closeMask._aiWrapped=true;
})();

const AI_PRESET_ENDPOINTS={
  bailian:"https://dashscope.aliyuncs.com/compatible-mode/v1",
  openai:"https://api.openai.com/v1",
  deepseek:"https://api.deepseek.com/v1",
  ollama:"http://localhost:11434/v1",
  claude:"https://dashscope.aliyuncs.com/apps/anthropic"
};
function syncAIPresetActive(){
  const ep=(($("aiEndpoint")&&$("aiEndpoint").value)||"").trim().replace(/\/+$/,"");
  document.querySelectorAll(".ai-preset-btn[data-preset]").forEach(btn=>{
    const key=btn.getAttribute("data-preset");
    const want=(AI_PRESET_ENDPOINTS[key]||"").replace(/\/+$/,"");
    btn.classList.toggle("active", !!want && ep===want);
  });
}
function setAINavDot(tab, on){
  const el=document.querySelector('[data-ai-nav-dot="'+tab+'"]');
  if(!el) return;
  if(on) el.removeAttribute("hidden"); else el.setAttribute("hidden","");
}
function updateAllAISettingsSummaries(){
  updateProviderPanelSummary();
  updateEmbedCardSummary();
  updateRerankCardSummary();
  updateWeKnoraCardSummary();
  updateRagPanelSummary();
  updateQualityPanelSummary();
  updateMcpCardSummary();
  updateSecurityPanelSummary();
  updateAISettingsNavDots();
}
function updateProviderPanelSummary(){
  const on=$("aiEnabled")&&$("aiEnabled").checked;
  const model=(($("aiModel")&&$("aiModel").value)||"").trim();
  const speechOn=!!(($("speechPreferCloud")&&$("speechPreferCloud").checked) || (($("speechSTTModel")&&$("speechSTTModel").value)||"").trim() || (($("speechTTSModel")&&$("speechTTSModel").value)||"").trim());
  const panel=$("providerPanelSummary");
  if(panel){
    const parts=[];
    if(on && model) parts.push(model);
    else if(on) parts.push(I18N.t("sre.enabled_state","已启用")||"已启用");
    else parts.push(I18N.t("sre.not_enabled","未启用")||"未启用");
    if(speechOn) parts.push(I18N.t("sre.ai_speech_on","语音")||"语音");
    panel.textContent=parts.join(" · ");
    panel.className="ai-card-summary"+(on?" on":"");
  }
  const chat=$("providerChatCardSummary");
  if(chat){ chat.textContent=model?(" · "+model):""; chat.className="ai-card-summary"+(model?" on":""); }
  const sp=$("providerSpeechCardSummary");
  if(sp){
    sp.textContent=speechOn?(" · "+(I18N.t("sre.enabled_state","已启用")||"已启用")):(" · "+(I18N.t("sre.not_enabled","未启用")||"未启用"));
    sp.className="ai-card-summary"+(speechOn?" on":"");
  }
}
function updateRagPanelSummary(){
  const emb=(($("embedModel")&&$("embedModel").value)||"").trim();
  const rr=(($("rerankModel")&&$("rerankModel").value)||"").trim();
  const wk=$("weknoraEnabled")&&$("weknoraEnabled").checked;
  const panel=$("ragPanelSummary"); if(!panel) return;
  const parts=[];
  if(emb) parts.push("Embed");
  if(rr) parts.push("Rerank");
  if(wk) parts.push("WeKnora");
  panel.textContent=parts.length?parts.join(" · "):(I18N.t("sre.not_enabled","未启用")||"未启用");
  panel.className="ai-card-summary"+((emb||rr||wk)?" on":"");
}
function updateQualityPanelSummary(){
  const sv=$("aiSelfVerify")&&$("aiSelfVerify").checked;
  const moa=(($("aiMoAModels")&&$("aiMoAModels").value)||"").trim();
  const price=parseFloat(($("aiInputPrice")&&$("aiInputPrice").value)||"")||0;
  const panel=$("qualityPanelSummary");
  if(panel){
    const parts=[];
    if(sv) parts.push("校验");
    if(moa) parts.push("MoA");
    if(price>0) parts.push("单价");
    panel.textContent=parts.length?parts.join(" · "):"—";
    panel.className="ai-card-summary"+((sv||moa||price>0)?" on":"");
  }
  const d=$("qualityDiagCardSummary");
  if(d){ d.textContent=(sv||moa)?(" · "+(I18N.t("sre.enabled_state","已启用")||"已启用")):""; d.className="ai-card-summary"+((sv||moa)?" on":""); }
  const c=$("qualityCostCardSummary");
  if(c){ c.textContent=price>0?(" · "+price):""; c.className="ai-card-summary"+(price>0?" on":""); }
  const r=$("qualityRouteCardSummary");
  const routeOn=!!(((($("aiCheapModel")&&$("aiCheapModel").value)||"").trim())||((($("aiTaskModels")&&$("aiTaskModels").value)||"").trim())||((($("aiActiveExperiment")&&$("aiActiveExperiment").value)||"").trim()));
  if(r){ r.textContent=routeOn?(" · "+(I18N.t("sre.configured","已配置")||"已配置")):""; r.className="ai-card-summary"+(routeOn?" on":""); }
}
function updateSecurityPanelSummary(){
  const term=!!AI_TERM_ENABLED;
  const mem=!!(($("disablePublicChatMemory")&&$("disablePublicChatMemory").checked) || ($("autoDefendEnabled")&&$("autoDefendEnabled").checked) || ($("selfEvolveEnabled")&&$("selfEvolveEnabled").checked));
  const panel=$("securityPanelSummary");
  if(panel){
    const parts=[];
    if(term) parts.push(I18N.t("sre.term_on","已开启")||"终端开");
    if(mem) parts.push(I18N.t("sre.ai_memory_gov","治理")||"治理");
    panel.textContent=parts.length?parts.join(" · "):(I18N.t("sre.term_off","未开启")||"未开启");
    panel.className="ai-card-summary"+(term||mem?" on":"");
  }
  const q=$("securityQuotaCardSummary");
  const quota=parseInt(($("aiDailyQuota")&&$("aiDailyQuota").value)||"0",10)||0;
  if(q){ q.textContent=quota>0?(" · "+quota+"/日"):""; }
  const m=$("securityMemoryCardSummary");
  if(m){ m.textContent=mem?(" · "+(I18N.t("sre.enabled_state","已启用")||"已启用")):""; m.className="ai-card-summary"+(mem?" on":""); }
  const t=$("securityTermCardSummary");
  if(t){
    t.textContent=term?(" · "+(I18N.t("sre.term_on","已开启")||"已开启")):(" · "+(I18N.t("sre.term_off","未开启")||"未开启"));
    t.className="ai-card-summary"+(term?" on":"");
  }
}
function updateAISettingsNavDots(){
  const providerOn=!!($("aiEnabled")&&$("aiEnabled").checked && ((($("aiModel")&&$("aiModel").value)||"").trim()));
  const ragOn=!!(((($("embedModel")&&$("embedModel").value)||"").trim()) || ($("weknoraEnabled")&&$("weknoraEnabled").checked));
  const qualityOn=!!(($("aiSelfVerify")&&$("aiSelfVerify").checked) || ((($("aiMoAModels")&&$("aiMoAModels").value)||"").trim()) || ((parseFloat(($("aiInputPrice")&&$("aiInputPrice").value)||"")||0)>0));
  const clients=typeof getMCPClientsList==="function" ? getMCPClientsList() : [];
  const integrateOn=!!(($("mcpEnabled")&&$("mcpEnabled").checked) || (clients||[]).some(c=>c.enabled));
  const securityOn=!!AI_TERM_ENABLED;
  setAINavDot("provider", providerOn);
  setAINavDot("rag", ragOn);
  setAINavDot("quality", qualityOn);
  setAINavDot("integrate", integrateOn);
  setAINavDot("observe", false);
  setAINavDot("security", securityOn);
}
function updateMcpCardSummary(){
  const on=$("mcpEnabled") && $("mcpEnabled").checked;
  const clients=getMCPClientsList();
  const nOn=clients.filter(c=>c.enabled).length;
  const nAll=clients.length;
  const serverTxt = on ? (I18N.t("sre.mcp_server_on","Server 开")||"Server 开") : (I18N.t("sre.mcp_server_off","Server 关")||"Server 关");
  const clientsTxt = nAll
    ? ((I18N.t("sre.mcp_clients_on","Clients")||"Clients")+" "+nOn+(nAll!==nOn?"/"+nAll:""))
    : (I18N.t("sre.mcp_clients_none","无 Client")||"无 Client");

  const panel=$("mcpCardSummary");
  if(panel){
    const parts=[serverTxt];
    if(nOn>0) parts.push((I18N.t("sre.mcp_clients_on","Clients")||"Clients")+" "+nOn);
    panel.textContent = parts.join(" · ");
    panel.className = "ai-card-summary" + ((on||nOn>0) ? " on" : "");
  }
  const srvSum=$("mcpServerCardSummary");
  if(srvSum){
    srvSum.textContent = " · " + serverTxt;
    srvSum.className = "ai-card-summary" + (on ? " on" : "");
  }
  const cliSum=$("mcpClientsCardSummary");
  if(cliSum){
    cliSum.textContent = " · " + clientsTxt;
    cliSum.className = "ai-card-summary" + (nOn>0 ? " on" : "");
  }
  refreshMcpClientConfig();
  if(typeof updateAISettingsNavDots==="function") updateAISettingsNavDots();
}

let MCP_CLIENTS = [];
function getMCPClientsList(){ return Array.isArray(MCP_CLIENTS) ? MCP_CLIENTS : []; }
function loadMCPClientsFromJSON(raw){
  MCP_CLIENTS = [];
  const s=String(raw||"").trim();
  if(s && s!=="****"){
    try{ const arr=JSON.parse(s); if(Array.isArray(arr)) MCP_CLIENTS=arr; }catch(_){}
  }
  renderMCPClientsList();
  syncMCPClientsHidden();
  updateMcpCardSummary();
}
function syncMCPClientsHidden(){
  const el=$("mcpClientsJSON");
  if(el) el.value = MCP_CLIENTS.length ? JSON.stringify(MCP_CLIENTS) : "";
}
function serializeMCPClientsJSON(){
  syncMCPClientsHidden();
  return ($("mcpClientsJSON")?.value||"").trim();
}
function splitCSV(s){
  return String(s||"").split(/[,，\s]+/).map(x=>x.trim()).filter(Boolean);
}
function renderMCPClientsList(){
  const box=$("mcpClientsList"); if(!box) return;
  const list=getMCPClientsList();
  if(!list.length){
    box.className="mcp-clients-list hint";
    box.textContent=I18N.t("sre.mcp_clients_empty","暂无外部 MCP Client");
    return;
  }
  box.className="mcp-clients-list";
  box.innerHTML=list.map((c,i)=>{
    const tools=Array.isArray(c.synced_tools)?c.synced_tools.length:0;
    const on=c.enabled?'<span class="badge ok">ON</span>':'<span class="badge">OFF</span>';
    const err=c.last_sync_error?`<div class="hint" style="color:var(--danger)">${esc(c.last_sync_error)}</div>`:"";
    return `<div class="mcp-client-row" data-idx="${i}">
      <div class="mcp-client-main">
        <strong>${esc(c.name||c.id||"")}</strong> ${on}
        <code class="mono">${esc(c.url||"")}</code>
        <span class="tag">${tools} tools</span>
      </div>
      ${err}
      <div class="mcp-client-acts">
        <button type="button" class="btn sm" data-mcp-edit="${i}">${esc(I18N.t("ui.edit","编辑"))}</button>
        <button type="button" class="btn sm ghost" data-mcp-del="${i}">${esc(I18N.t("ui.delete","删除"))}</button>
      </div>
    </div>`;
  }).join("");
}
function openMCPClientEditor(idx){
  setMcpSectionCollapsed("clients", false);
  const ed=$("mcpClientEditor"); if(!ed) return;
  ed.hidden=false;
  const c = (idx!=null && MCP_CLIENTS[idx]) ? MCP_CLIENTS[idx] : {enabled:true,timeout_sec:30,headers:{}};
  $("mcpClientEditId").value = c.id||"";
  $("mcpClientName").value = c.name||"";
  $("mcpClientEnabled").checked = c.enabled!==false;
  $("mcpClientURL").value = c.url||"";
  const auth=(c.headers&& (c.headers.Authorization||c.headers.authorization))||"";
  $("mcpClientAuth").value = auth;
  $("mcpClientTimeout").value = c.timeout_sec||30;
  $("mcpClientAllow").value = (c.tool_allowlist||[]).join(",");
  $("mcpClientBlock").value = (c.tool_blocklist||[]).join(",");
  const prev=$("mcpClientToolsPreview");
  if(prev){
    const tools=c.synced_tools||[];
    prev.innerHTML = tools.length
      ? `<div class="table-wrap"><table class="data"><thead><tr><th>tool</th><th></th><th>desc</th></tr></thead><tbody>${
          tools.map(t=>`<tr><td class="mono">${esc(t.name||"")}</td><td>${t.blocked?"blocked":"ok"}</td><td>${esc(t.description||"")}</td></tr>`).join("")
        }</tbody></table></div>`
      : "";
  }
  const res=$("mcpClientTestResult"); if(res){ res.textContent=""; res.className="ai-test-result"; }
}
function readMCPClientEditor(){
  const id=($("mcpClientEditId")?.value||"").trim();
  const name=($("mcpClientName")?.value||"").trim();
  const url=($("mcpClientURL")?.value||"").trim();
  const auth=($("mcpClientAuth")?.value||"").trim();
  const timeout=parseInt($("mcpClientTimeout")?.value,10)||30;
  const headers={};
  if(auth) headers.Authorization=auth;
  // Preserve other headers from existing entry
  const existing = id ? MCP_CLIENTS.find(x=>x.id===id) : null;
  if(existing && existing.headers){
    Object.keys(existing.headers).forEach(k=>{
      if(k.toLowerCase()==="authorization") return;
      headers[k]=existing.headers[k];
    });
  }
  return {
    id: id || undefined,
    name: name||url||"mcp",
    enabled: !!($("mcpClientEnabled")?.checked),
    url,
    headers,
    timeout_sec: timeout,
    tool_allowlist: splitCSV($("mcpClientAllow")?.value),
    tool_blocklist: splitCSV($("mcpClientBlock")?.value),
    synced_tools: existing && existing.synced_tools ? existing.synced_tools : [],
    last_sync_unix: existing && existing.last_sync_unix ? existing.last_sync_unix : 0,
    last_sync_error: existing && existing.last_sync_error ? existing.last_sync_error : ""
  };
}
function saveMCPClientEditorToList(){
  const c=readMCPClientEditor();
  if(!c.url){ toast(I18N.t("sre.mcp_need_url","请填写 MCP URL"),"err"); return; }
  if(!c.id){
    c.id = (c.name||"mcp").toLowerCase().replace(/[^a-z0-9_-]+/g,"_").slice(0,24) + "_" + Math.random().toString(16).slice(2,6);
  }
  const i=MCP_CLIENTS.findIndex(x=>x.id===c.id);
  if(i>=0) MCP_CLIENTS[i]=Object.assign({}, MCP_CLIENTS[i], c);
  else MCP_CLIENTS.push(c);
  syncMCPClientsHidden();
  renderMCPClientsList();
  updateMcpCardSummary();
  const ed=$("mcpClientEditor"); if(ed) ed.hidden=true;
  toast(I18N.t("sre.mcp_client_saved","已写入列表，请再点底部「保存」持久化"),"ok"); markAIConfigDirty();
}
async function testMCPClientEditor(){
  const c=readMCPClientEditor();
  const el=$("mcpClientTestResult");
  if(!c.url){ if(el){ el.textContent="✗ URL 必填"; el.className="ai-test-result err"; } return; }
  if(el){ el.textContent=I18N.t("sre.testing","测试中…"); el.className="ai-test-result testing"; }
  try{
    const r=await fetch(`${API}/ai/mcp-clients/test`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(c)});
    const j=await r.json().catch(()=>({}));
    if(!j.ok){ if(el){ el.textContent="✗ "+(j.error||"失败"); el.className="ai-test-result err"; } return; }
    if(el){ el.textContent=`✓ ${j.tool_count||0} tools · allowed ${j.allowed_count||0} · ${j.latency_ms||0}ms`; el.className="ai-test-result ok"; }
    const prev=$("mcpClientToolsPreview");
    if(prev && Array.isArray(j.tools)){
      prev.innerHTML=`<div class="table-wrap"><table class="data"><thead><tr><th>tool</th><th></th><th>desc</th></tr></thead><tbody>${
        j.tools.map(t=>`<tr><td class="mono">${esc(t.name||"")}</td><td>${t.blocked?"blocked":"ok"}</td><td>${esc(t.description||"")}</td></tr>`).join("")
      }</tbody></table></div>`;
    }
  }catch(e){
    if(el){ el.textContent="✗ "+e; el.className="ai-test-result err"; }
  }
}
async function syncMCPClientEditor(){
  const c=readMCPClientEditor();
  const el=$("mcpClientTestResult");
  if(!c.url){ if(el){ el.textContent="✗ URL 必填"; el.className="ai-test-result err"; } return; }
  if(el){ el.textContent=I18N.t("sre.mcp_syncing","同步中…"); el.className="ai-test-result testing"; }
  try{
    // Ensure id exists before sync so server can upsert
    if(!c.id){
      c.id = (c.name||"mcp").toLowerCase().replace(/[^a-z0-9_-]+/g,"_").slice(0,24) + "_" + Math.random().toString(16).slice(2,6);
      $("mcpClientEditId").value=c.id;
    }
    const r=await fetch(`${API}/ai/mcp-clients/sync`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(c)});
    const j=await r.json().catch(()=>({}));
    if(j.client){
      const nc=j.client;
      const i=MCP_CLIENTS.findIndex(x=>x.id===nc.id);
      if(i>=0) MCP_CLIENTS[i]=Object.assign({}, MCP_CLIENTS[i], nc, {headers: Object.assign({}, MCP_CLIENTS[i].headers||{}, nc.headers||{})});
      else MCP_CLIENTS.push(nc);
      // keep auth local if masked
      if(c.headers && c.headers.Authorization && !String(c.headers.Authorization).includes("****")){
        const idx=MCP_CLIENTS.findIndex(x=>x.id===nc.id);
        if(idx>=0){
          MCP_CLIENTS[idx].headers=MCP_CLIENTS[idx].headers||{};
          MCP_CLIENTS[idx].headers.Authorization=c.headers.Authorization;
        }
      }
      syncMCPClientsHidden();
      renderMCPClientsList();
      updateMcpCardSummary();
      openMCPClientEditor(MCP_CLIENTS.findIndex(x=>x.id===nc.id));
    }
    if(!j.ok){ if(el){ el.textContent="✗ "+(j.error||"同步失败"); el.className="ai-test-result err"; } return; }
    if(el){ el.textContent=`✓ synced ${j.tool_count||0} tools`; el.className="ai-test-result ok"; }
    toast(I18N.t("sre.mcp_synced","工具已同步并写入配置"),"ok");
  }catch(e){
    if(el){ el.textContent="✗ "+e; el.className="ai-test-result err"; }
  }
}

function mcpEndpointURL(){
  try{ return new URL("/api/v1/mcp", location.origin).href; }catch(_){ return (location.origin||"")+"/api/v1/mcp"; }
}
function refreshMcpClientConfig(){
  const el=$("mcpClientConfig"); if(!el) return;
  const tok=($("mcpToken")?.value||"").trim();
  const showTok = tok && !tok.includes("****") ? tok : "<你的 MCP Token>";
  const url=mcpEndpointURL();
  el.value=JSON.stringify({
    mcpServers:{
      "aiops-monitor":{
        url,
        headers:{ Authorization:"Bearer "+showTok }
      }
    }
  },null,2);
}
async function copyMcpClientConfig(){
  refreshMcpClientConfig();
  const el=$("mcpClientConfig"); if(!el) return;
  try{
    if(navigator.clipboard&&navigator.clipboard.writeText){ await navigator.clipboard.writeText(el.value); }
    else{ el.focus(); el.select(); document.execCommand("copy"); }
    if(typeof toast==="function") toast(I18N.t("sre.mcp_cfg_copied","已复制 MCP 客户端配置"),"ok");
  }catch(e){
    if(typeof toast==="function") toast(I18N.t("sre.copy_failed","复制失败")+"："+e,"err");
  }
}
function updateWeKnoraCardSummary(){
  const el=$("weknoraCardSummary"); if(!el) return;
  const on=$("weknoraEnabled") && $("weknoraEnabled").checked;
  const url=($("weknoraURL")?.value||"").trim();
  el.textContent = on ? (url ? " · 已启用" : " · 已启用·待填 URL") : " · 未启用";
  el.className = "ai-card-summary" + (on ? " on" : "");
  if(typeof updateRagPanelSummary==="function") updateRagPanelSummary();
}

// WeKnora knowledge-search 连通性测试
let _aiWeKnoraTestBusy=false;
async function testAIWeKnoraConfig(){
  if(_aiWeKnoraTestBusy) return;
  const el=$("aiWeKnoraTestResult");
  _aiWeKnoraTestBusy=true;
  const testBtn=$("aiWeKnoraTestBtn"); if(testBtn) testBtn.disabled=true;
  if(el){ el.textContent="WeKnora "+I18N.t("sre.testing","测试中…"); el.className="ai-test-result testing"; }
  const body={
    weknora_enabled:true,
    weknora_url:($("weknoraURL")?.value||"").trim(),
    weknora_api_key:$("weknoraKey")?.value||"",
    weknora_knowledge_base_ids:($("weknoraKBIDs")?.value||"").trim()
  };
  try{
    const r=await fetch(`${API}/ai/test-weknora`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    const j=await r.json().catch(()=>({}));
    if(!el) return;
    if(j.ok){
      const scope=j.scope||((j.kb_count!=null)?(`${j.kb_count} 个知识库`):"");
      const strat=j.strategy?` · ${j.strategy}`:"";
      el.textContent=`✓ WeKnora 可用 · ${j.latency_ms||0}ms · ${scope}${strat}\n${j.preview||""}`;
      el.className="ai-test-result ok"; el.style.whiteSpace="pre-wrap";
    } else {
      el.textContent="✗ WeKnora "+(j.error||I18N.t("sre.test_failed","测试失败"));
      el.className="ai-test-result err"; el.style.whiteSpace="pre-wrap";
    }
  }catch(e){ if(el){ el.textContent="✗ WeKnora "+I18N.t("sre.request_failed","请求失败")+"："+e; el.className="ai-test-result err"; } }
  finally{ _aiWeKnoraTestBusy=false; if(testBtn) testBtn.disabled=false; }
}

let _aiWeKnoraListBusy=false;
async function listAIWeKnoraKBs(){
  if(_aiWeKnoraListBusy) return;
  const el=$("aiWeKnoraTestResult");
  _aiWeKnoraListBusy=true;
  const btn=$("aiWeKnoraListBtn"); if(btn) btn.disabled=true;
  if(el){ el.textContent="正在拉取知识库列表…"; el.className="ai-test-result testing"; }
  const body={
    weknora_url:($("weknoraURL")?.value||"").trim(),
    weknora_api_key:$("weknoraKey")?.value||""
  };
  try{
    const r=await fetch(`${API}/ai/list-weknora-kbs`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    const j=await r.json().catch(()=>({}));
    if(!el) return;
    if(j.ok){
      const rows=(j.knowledge_bases||[]).map(kb=>{
        const n=kb.name||kb.id;
        const cnt=(kb.knowledge_count!=null)?` · 文档 ${kb.knowledge_count}`:"";
        return `• ${n} (${kb.id})${cnt}`;
      });
      el.textContent=`✓ 可见知识库 ${j.count||0} 个 · ${j.latency_ms||0}ms\n`+(rows.join("\n")||"（空）")+"\n提示：限定 ID 留空即可自动跨上述全部库检索；也可复制 ID 填入限定框。";
      el.className="ai-test-result ok"; el.style.whiteSpace="pre-wrap";
    } else {
      el.textContent="✗ "+(j.error||"拉取失败"); el.className="ai-test-result err"; el.style.whiteSpace="pre-wrap";
    }
  }catch(e){ if(el){ el.textContent="✗ 请求失败："+e; el.className="ai-test-result err"; } }
  finally{ _aiWeKnoraListBusy=false; if(btn) btn.disabled=false; }
}

// AI 重排(rerank)模型连接测试
let _aiRerankTestBusy=false;
async function testAIRerankConfig(){
  if(_aiRerankTestBusy) return;
  const el=$("aiRerankTestResult");
  _aiRerankTestBusy=true;
  const testBtn=$("aiRerankTestBtn"); if(testBtn) testBtn.disabled=true;
  if(el){ el.textContent=I18N.t("sre.ai_rerank_model","重排模型")+" "+I18N.t("sre.testing","测试中…"); el.className="ai-test-result testing"; }
  const body={enabled:true,
    rerank_endpoint:$("rerankEndpoint").value.trim(),
    rerank_api_key:$("rerankKey").value,
    rerank_model:$("rerankModel").value.trim(),
    embed_endpoint:$("embedEndpoint").value.trim(),
    embed_api_key:$("embedKey").value,
    endpoint:$("aiEndpoint").value.trim(),
    api_key:$("aiKey").value
  };
  try{
    const r=await fetch(`${API}/ai/test-rerank`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    const j=await r.json().catch(()=>({}));
    if(!el) return;
    if(j.ok){ el.textContent=`✓ ${I18N.t("sre.rerank_model_ok","重排模型可用")} · ${j.latency_ms||0}ms · ${j.model||""}`; el.className="ai-test-result ok"; }
    else { el.textContent="✗ "+I18N.t("sre.ai_rerank_model","重排模型")+" "+(j.error||I18N.t("sre.test_failed","测试失败")); el.className="ai-test-result err"; }
  }catch(e){ if(el){ el.textContent="✗ "+I18N.t("sre.ai_rerank_model","重排模型")+" "+I18N.t("sre.request_failed","请求失败")+"："+e; el.className="ai-test-result err"; } }
  finally{ _aiRerankTestBusy=false; if(testBtn) testBtn.disabled=false; }
}

// 生成高强度随机令牌（32 字节 CSPRNG → base64url）并自动填入
function genStrongToken(nbytes){
  const arr=new Uint8Array(nbytes||32);
  (window.crypto||window.msCrypto).getRandomValues(arr);
  let bin=""; for(let i=0;i<arr.length;i++) bin+=String.fromCharCode(arr[i]);
  return btoa(bin).replace(/\+/g,"-").replace(/\//g,"_").replace(/=+$/,"");
}

// 更新向量化模型卡片摘要
function updateEmbedCardSummary(){
  const summary=$("embedCardSummary"); if(!summary) return;
  const model=(($("embedModel")&&$("embedModel").value)||"").trim();
  if(model){ summary.textContent=` · ${I18N.t("sre.configured","已配置")}：${model}`; summary.className="ai-card-summary on"; }
  else { summary.textContent=""; summary.className="ai-card-summary"; }
  if(typeof updateRagPanelSummary==="function") updateRagPanelSummary();
}

// 更新重排模型卡片摘要
function updateRerankCardSummary(){
  const summary=$("rerankCardSummary"); if(!summary) return;
  const model=(($("rerankModel")&&$("rerankModel").value)||"").trim();
  summary.textContent=model?` · ${I18N.t("sre.enabled_state","已启用")}：${model}`:" · "+I18N.t("sre.not_enabled","未启用");
  summary.className="ai-card-summary"+(model?" on":"");
  if(typeof updateRagPanelSummary==="function") updateRagPanelSummary();
}

// 过滤 AI 输出中的敏感信息（密钥 / 密码 / token）。代码与命令予以保留、交由 Markdown 渲染
// 展示——工具调用 JSON 已在后端剥离，这里仅对结尾残留兜底，不再误删正文里的命令/代码。
function filterDisplayContent(text){
  if(!text) return text;
  let t=text;
  t=t.replace(/\{\s*"tool_calls"[\s\S]*?\}\s*$/g,''); // 兜底：结尾残留的 tool_calls JSON
  t=t.replace(/\b(sk-[a-zA-Z0-9_-]{20,})\b/g,I18N.t("sre.redacted_key","[已隐藏密钥]")); // API 密钥
  t=t.replace(/\b(api_key|apikey|secret|password|passwd|token)\s*[:=]\s*['"]?[^\s'"]+['"]?/gi,'$1='+I18N.t("sre.redacted","[已隐藏]"));
  t=t.replace(/\bhermes(?:\s+agent)?\b/gi,"智能运维服务");
  t=t.replace(/hermes_auto_approve/gi,"ai_auto_approve");
  t=t.replace(/hermes_terminal_enabled/gi,"ai_terminal_enabled");
  t=t.replace(/hermes_enabled/gi,"ai_agent_enabled");
  t=t.replace(/\bhermes(?:\s+agent)?\b/gi,"智能运维服务");
  // Defense-in-depth: replace known host ids with HostPicker labels when available.
  try{
    const hosts=(typeof HOSTS!=="undefined"&&Array.isArray(HOSTS))?HOSTS
      :(typeof window!=="undefined"&&Array.isArray(window.HOSTS))?window.HOSTS:[];
    if(hosts.length&&typeof HostPicker!=="undefined"&&HostPicker.hostTitle){
      const pairs=hosts.map(h=>[h&&h.id,HostPicker.hostTitle(h)]).filter(p=>p[0]&&p[1]);
      pairs.sort((a,b)=>String(b[0]).length-String(a[0]).length);
      pairs.forEach(([id,lab])=>{ if(id) t=t.split(id).join(lab); });
    }
  }catch(_e){}
  return t.trim();
}
// 轻量 Markdown 渲染：先转义 HTML 防 XSS，再套用有限格式（加粗/斜体/有序无序列表/换行）。
// 输入应为已经 filterDisplayContent 过滤的文本（代码块/密钥已剔除）。
// ===== 轻量语法高亮（CSP 安全·零依赖）：常见运维语言的 注释/字符串/数字/关键字 =====
const HL_KW = {
  python:"def class return if elif else for while import from as with try except finally raise in is and or not None True False lambda pass break continue yield assert del async await global nonlocal self print",
  py:"def class return if elif else for while import from as with try except finally raise in is and or not None True False lambda pass break continue yield del async await self",
  bash:"if then else elif fi for while do done case esac function in return export local echo cd exit set unset read source",
  sh:"if then else elif fi for while do done case esac function in return export local echo cd exit set unset read source",
  shell:"if then else elif fi for while do done case esac function in return export local echo cd exit",
  javascript:"function return if else for while const let var new class extends import export default async await try catch finally throw typeof instanceof in of null undefined true false this switch case break continue delete void",
  js:"function return if else for while const let var new class import export default async await try catch throw null undefined true false this switch case break continue",
  typescript:"function return if else for while const let var new class extends implements interface type enum import export default async await try catch finally throw typeof in of null undefined true false this public private protected readonly",
  ts:"function return if else for while const let var new class interface type import export async await try catch throw null undefined true false public private readonly",
  go:"func package import return if else for range var const type struct interface map chan go defer select switch case break continue fallthrough nil true false make new append len cap panic recover",
  sql:"select from where insert update delete into values set create table drop alter add index join left right inner outer full on group by order having limit offset as and or not null is distinct count sum avg min max like between union all",
  json:"true false null",
  yaml:"true false null yes no on off",
  yml:"true false null yes no on off",
  java:"public private protected class interface extends implements return if else for while new import package void int long double float boolean char String null true false this static final abstract try catch finally throw throws",
  c:"int char float double void long short unsigned signed return if else for while do struct union enum typedef const static sizeof break continue switch case default goto NULL",
  cpp:"int char float double void return if else for while class struct namespace using template typename const static public private protected virtual true false nullptr new delete this",
  rust:"fn let mut const struct enum impl trait pub use mod match if else for while loop return break continue self Self Some None Ok Err true false as ref move async await where",
};
const HL_LINE = { python:"#",py:"#",bash:"#",sh:"#",shell:"#",yaml:"#",yml:"#",toml:"#",ini:"#",conf:"#",sql:"--",javascript:"//",js:"//",typescript:"//",ts:"//",go:"//",java:"//",c:"//",cpp:"//",rust:"//" };
const HL_BLOCK = { javascript:1,js:1,typescript:1,ts:1,go:1,java:1,c:1,cpp:1,rust:1,css:1 };
function _hlEsc(s){ return String(s).replace(/[&<>]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;"}[c])); }
function _hlReEsc(s){ return s.replace(/[.*+?^${}()|[\]\\]/g,"\\$&"); }
function highlightCode(code, lang){
  lang=String(lang||"").toLowerCase();
  const kw=new Set((HL_KW[lang]||"").split(/\s+/).filter(Boolean));
  const line=Object.prototype.hasOwnProperty.call(HL_LINE,lang)?HL_LINE[lang]:null;
  const block=!!HL_BLOCK[lang];
  const parts=[];
  if(block) parts.push("\\/\\*[\\s\\S]*?\\*\\/");
  if(line) parts.push(_hlReEsc(line)+"[^\\n]*");
  parts.push('"(?:\\\\.|[^"\\\\\\n])*"',"'(?:\\\\.|[^'\\\\\\n])*'","`(?:\\\\.|[^`\\\\])*`");
  parts.push("\\b\\d[\\d._]*\\b","[A-Za-z_$][A-Za-z0-9_$]*");
  const re=new RegExp(parts.join("|"),"g");
  let out="",last=0,m;
  while((m=re.exec(code))){
    out+=_hlEsc(code.slice(last,m.index));
    const tok=m[0]; last=m.index+tok.length;
    let cls="";
    if(block&&tok.startsWith("/*")) cls="tok-com";
    else if(line&&tok.startsWith(line)) cls="tok-com";
    else if(tok[0]==='"'||tok[0]==="'"||tok[0]==="`") cls="tok-str";
    else if(tok[0]>="0"&&tok[0]<="9") cls="tok-num";
    else if(kw.has(tok)) cls="tok-kw";
    out+=cls?`<span class="${cls}">${_hlEsc(tok)}</span>`:_hlEsc(tok);
  }
  out+=_hlEsc(code.slice(last));
  return out;
}
function renderAIMarkdown(raw){
  if(!raw) return "";
  // 1) 先抽出围栏代码块占位，避免其内部被当作 Markdown/HTML 处理
  const blocks=[];
  let t=raw.replace(/```([a-zA-Z0-9_+#-]*)\n?([\s\S]*?)```/g,(m,lang,code)=>{
    blocks.push({lang:(lang||"").toLowerCase(), code:code.replace(/\n+$/,"")});
    return "SNTLCB"+(blocks.length-1)+"SNTL";
  });
  t=esc(t); // 2) 转义 HTML，杜绝注入
  // 3) Markdown 链接：看板协议 / http(s) 可点；其它仅保留文字
  t=t.replace(/\[([^\]\n]+)\]\(([^)\n]+)\)/g,(m,label,url)=>{
    const u=String(url||"").trim();
    const dash=u.match(/^aiops:\/\/dashboard\/([^/?#\s]+)$/i) || u.match(/^#\/?(?:dashboards?|board)\/([^/?#\s]+)$/i);
    if(dash){
      return `<a href="#" class="ai-dash-link" data-dash="${esc(decodeURIComponent(dash[1]))}" title="${I18N.t("sre.open_dashboard","打开看板")}">${label}</a>`;
    }
    if(/^https?:\/\//i.test(u)){
      return `<a href="${esc(u)}" class="ai-ext-link" target="_blank" rel="noopener noreferrer">${label}</a>`;
    }
    return label;
  });
  t=t.replace(/`([^`\n]+)`/g,"<code>$1</code>"); // 行内代码（内容已转义）
  t=t.replace(/\*\*([^*\n]+)\*\*/g,"<strong>$1</strong>"); // 4) 加粗 / 斜体
  t=t.replace(/__([^_\n]+)__/g,"<strong>$1</strong>");
  t=t.replace(/(^|[^*])\*([^*\n]+)\*(?!\*)/g,"$1<em>$2</em>");
  const lines=t.split("\n"); // 5) 标题 / 引用 / 分割线 / 列表 / 段落
  let html="",inList=false,listTag="ul";
  const close=()=>{ if(inList){ html+="</"+listTag+">"; inList=false; } };
  for(const line of lines){
    if(line.indexOf("SNTLCB")>=0){ close(); html+=line; continue; } // 代码块占位
    if(/^\s*([-*_])\1{2,}\s*$/.test(line)){ close(); html+="<hr class='ai-hr'>"; continue; } // 分割线 --- *** ___
    const h=line.match(/^\s*(#{1,6})\s*(.*)$/); // 标题 → 样式化，绝不残留 ### 字面量
    if(h){ close(); const tx=h[2].trim(); if(tx) html+=`<div class="ai-h ai-h${Math.min(h[1].length,4)}">${tx}</div>`; continue; }
    const bq=line.match(/^\s*&gt;\s?(.*)$/); // 引用（esc 后 > 变 &gt;）
    if(bq){ close(); html+=`<blockquote class="ai-bq">${bq[1]}</blockquote>`; continue; }
    // P3 诊断置信度：整行以「置信度：高/中/低」起头时渲染为彩色徽章（容忍前置的 <strong>/*/> 标记）
    if(/^\s*(?:<[^>]+>|[*>\s])*置信度\s*[:：]/.test(line)){
      const cm=line.match(/置信度\s*[:：]\s*(?:<[^>]+>|[*\s])*(高|中|低)/);
      if(cm){ close(); const lv=cm[1]; const cls=lv==="高"?"high":(lv==="低"?"low":"mid");
        html+=`<div class="ai-confidence ${cls}"><span class="ai-conf-badge">🎯 置信度 ${lv}</span></div>`; continue; }
    }
    const ul=line.match(/^\s*[-*•·]\s+(.+)$/);
    const ol=line.match(/^\s*\d+[.)]\s+(.+)$/);
    if(ul){ if(!inList||listTag!=="ul"){ close(); html+="<ul>"; inList=true; listTag="ul"; } html+="<li>"+ul[1]+"</li>"; }
    else if(ol){ if(!inList||listTag!=="ol"){ close(); html+="<ol>"; inList=true; listTag="ol"; } html+="<li>"+ol[1]+"</li>"; }
    else { close(); html+=(line.trim()==="")?"":("<div>"+line+"</div>"); }
  }
  close();
  html=html.replace(/SNTLCB(\d+)SNTL/g,(m,i)=>{ const b=blocks[+i]||{code:""}; const lang=b.lang||I18N.t("sre.code","代码"); // 6) 还原代码块：语言标签 + 独立复制按钮
    return "<div class=\"ai-code-wrap\"><div class=\"ai-code-head\"><span class=\"ai-code-lang\">"+esc(lang)+"</span><button class=\"ai-code-copy\" type=\"button\" title=\""+I18N.t("sre.copy_code","复制代码")+"\">"+I18N.t("sre.copy","复制")+"</button></div><pre class=\"ai-code\"><code>"+highlightCode(b.code,b.lang)+"</code></pre></div>"; });
  return html;
}
// AI 对话消息区：判断是否贴底（供流式时决定要不要自动滚动）
function aiChatStick(){ const log=$("aiChatLog"); return log ? (log.scrollHeight-log.scrollTop-log.clientHeight<80) : true; }
function aiChatToBottom(){ const log=$("aiChatLog"); if(log) log.scrollTop=log.scrollHeight; }
// 统一「AI 对话」——单窗口,后端走 Sreyun 自主运维 Agent（能对话 + 自动调用工具,
// 不需要工具时自动退化成纯对话）。模型与 AI 设置共用同一套配置。
let AI_CHAT_SESSION=0;   // Sreyun 服务端会话 id（0=新会话）
let AI_CHAT_HISTORY=[];  // 前端侧会话历史 {role,content,actions?}：兜底传后端 + 本地记忆
const AI_CHAT_INTRO=`<div class="ai-welcome"><div class="ai-welcome-icon">🤖</div><div class="ai-welcome-title">${I18N.t("sre.chat_intro_title","AI 运维助手已就绪")}</div><div class="ai-welcome-sub">${I18N.t("sre.chat_intro_sub","全局 AI 入口：看板组件调用、任意界面调度、趋势图表与指标下钻、安全自动防御、自我进化与记忆优化、诊断加固、报告导出——一句话即可调度。也可上传 📄 文档 / 🔗 网页辅助分析。")}</div><div class="ai-cap-chips"><span class="ai-cap-chip">看板组件</span><span class="ai-cap-chip">界面调度</span><span class="ai-cap-chip">安全防御</span><span class="ai-cap-chip">自我进化</span><span class="ai-cap-chip">趋势图表</span><span class="ai-cap-chip">导出报告</span></div></div><div id="aiChatSuggest" class="ai-suggest"></div>`;

function aiChatActionKey(a){
  return (a.type||"")+"|"+(a.id||"")+"|"+(a.title||"")+"|"+(a.label||"");
}
function parseAIChatActions(raw){
  if(!raw) return [];
  if(Array.isArray(raw)) return raw.filter(a=>a&&a.type);
  if(typeof raw==="string"){
    try{ const j=JSON.parse(raw); return Array.isArray(j)?j.filter(a=>a&&a.type):[]; }catch(e){ return []; }
  }
  return [];
}
function historyForAIChatAPI(hist){
  return (hist||[]).map(m=>{
    const item={role:m.role,content:m.content||""};
    const acts=parseAIChatActions(m.actions);
    if(acts.length) item.actions=JSON.stringify(acts);
    return item;
  });
}
/** AI 会话趋势图状态：支持预测开关重绘与放大预览 */
let AI_CHAT_CHARTS = {};
let AI_CHAT_CHART_SPECS = {};
const AI_CHAT_FC_SCOPE = "ai-chat";
const AI_CHART_ENLARGE_SVG = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7"/></svg>`;

function ensureAIChatForecastDefault(){
  if(!window._FC_ON) window._FC_ON = {};
  if(!Object.prototype.hasOwnProperty.call(window._FC_ON, AI_CHAT_FC_SCOPE)){
    window._FC_ON[AI_CHAT_FC_SCOPE] = true; // 会话趋势默认开启预测，便于一眼看到未来走势
  }
}

function renderAIChatWidgets(actions){
  if(!actions||!actions.length) return "";
  ensureAIChatForecastDefault();
  const seen=new Set();
  let html="";
  for(const a of actions){
    if(!a||!a.type) continue;
    const key=aiChatActionKey(a);
    if(seen.has(key)) continue;
    seen.add(key);
    if(a.type==="show_chart"){
      const cid="aiChart_"+(a.id||key.replace(/\|/g,"_"));
      const samples=a.chart&&Array.isArray(a.chart.samples)?a.chart.samples:[];
      const series=a.chart&&Array.isArray(a.chart.series)?a.chart.series:[];
      const empty=!a.chart||!samples.length||!series.length;
      const tools=empty?"":`<div class="ai-chart-tools">
        ${typeof forecastChipHTML==="function"?forecastChipHTML(AI_CHAT_FC_SCOPE):""}
        <button type="button" class="chart-enlarge" data-ai-chart-zoom="${esc(cid)}" title="${esc(I18N.t("ui.zoom_preview","放大预览"))}">${AI_CHART_ENLARGE_SVG}</button>
      </div>`;
      html+=`<div class="ai-chart-card" data-ai-chart="${esc(a.id||"")}" data-ai-chart-cid="${esc(cid)}">
        <div class="ai-chart-head"><span class="ai-chart-title">${esc(a.title||a.label||"趋势图")}</span>${tools}</div>
        ${empty
          ?`<div class="ai-chart-empty">${esc(I18N.t("sre.chart_empty","图表数据不可用或已过期，请重试生成"))}</div>`
          :`<div class="ai-chart-wrap"><canvas id="${cid}" height="210"></canvas></div>`}
      </div>`;
    } else if(a.type==="show_stat"){
      const unit=a.unit||"";
      const val=Number(a.value);
      const thr=a.thresholds||{};
      let tone="ok";
      if(Number.isFinite(val)){
        if(thr.crit!=null && val>=Number(thr.crit)) tone="crit";
        else if(thr.warn!=null && val>=Number(thr.warn)) tone="warn";
      }
      const sparkVals=Array.isArray(a.sparkline)?a.sparkline.map(p=>Array.isArray(p)?Number(p[1]):Number(p)).filter(n=>Number.isFinite(n)):[];
      const spark=typeof svgSparkline==="function"&&sparkVals.length>1?svgSparkline(sparkVals, tone==="crit"?"#ef4d5a":(tone==="warn"?"#f59e0b":"#4c8dff")):"";
      html+=`<div class="ai-stat-card ${tone}">
        <div class="ai-stat-label">${esc(a.title||a.label||"指标")}</div>
        <div class="ai-stat-value">${Number.isFinite(val)?val.toFixed(unit==="%"||unit===""?1:2):"—"}<span class="ai-stat-unit">${esc(unit)}</span></div>
        ${spark?`<div class="ai-stat-spark">${spark}</div>`:""}
      </div>`;
    } else if(a.type==="show_table"){
      const cols=Array.isArray(a.columns)?a.columns:[];
      const rows=Array.isArray(a.rows)?a.rows.slice(0,30):[];
      const head=cols.map(c=>`<th>${esc(String(c))}</th>`).join("");
      const body=rows.map(r=>{
        const cells=cols.map(c=>{
          const v=r&&Object.prototype.hasOwnProperty.call(r,c)?r[c]:"";
          return `<td>${esc(v==null?"":String(v))}</td>`;
        }).join("");
        return `<tr>${cells}</tr>`;
      }).join("");
      html+=`<div class="ai-table-card">
        <div class="ai-chart-head"><span class="ai-chart-title">${esc(a.title||a.label||"表格")}</span></div>
        <div class="ai-table-wrap"><table><thead><tr>${head}</tr></thead><tbody>${body||`<tr><td colspan="${Math.max(cols.length,1)}">暂无数据</td></tr>`}</tbody></table></div>
      </div>`;
    } else if(a.type==="show_logs"){
      const lines=Array.isArray(a.lines)?a.lines.slice(0,40):[];
      const body=lines.map(ln=>{
        const ts=ln&&ln.ts?esc(String(ln.ts)):"";
        const text=ln&&ln.line!=null?esc(String(ln.line)):(ln?esc(String(ln)):"");
        return `<div class="ai-log-line">${ts?`<span class="ai-log-ts">${ts}</span>`:""}<span class="ai-log-text">${text}</span></div>`;
      }).join("");
      html+=`<div class="ai-logs-card">
        <div class="ai-chart-head"><span class="ai-chart-title">${esc(a.title||a.label||"日志")}</span></div>
        <div class="ai-logs-wrap">${body||`<div class="ai-log-line"><span class="ai-log-text">暂无日志</span></div>`}</div>
      </div>`;
    }
  }
  return html;
}
function renderAIChatActions(actions){
  if(!actions||!actions.length) return "";
  const seen=new Set();
  const items=[];
  for(const a of actions){
    if(!a||!a.type) continue;
    // 图表/指标卡/表格/日志已在 widgets 区渲染，按钮区只留可点击动作
    if(a.type==="show_chart"||a.type==="show_stat"||a.type==="show_table"||a.type==="show_logs") continue;
    const key=aiChatActionKey(a);
    if(seen.has(key)) continue;
    seen.add(key);
    items.push(a);
  }
  if(!items.length) return "";
  return `<div class="ai-action-cards">`+items.map((a,i)=>{
    const label=esc(a.label||a.type);
    return `<button type="button" class="ai-action-card" data-ai-act="${i}">${label}</button>`;
  }).join("")+`</div>`;
}
function normalizeAIChatSeries(raw){
  return (Array.isArray(raw)?raw:[]).map(s=>({
    key:s.key, label:s.label||s.key, color:s.color||"#4c8dff",
    dashed:!!s.dashed || s.kind==="forecast",
    kind:s.kind||(s.dashed?"forecast":"history"),
    fmt: v=> Number.isFinite(v)?(Math.abs(v)>=100?v.toFixed(0):v.toFixed(2)): "-"
  }));
}

function aiChatSeriesForToggle(series, fcOn){
  if(fcOn) return series;
  return (series||[]).filter(s=>s && s.kind!=="forecast" && !s.dashed);
}

async function paintAIChatChart(cid, spec){
  if(!spec||typeof createChart!=="function") return null;
  const canvas=$(cid) || document.getElementById(cid);
  if(!canvas) return null;
  ensureAIChatForecastDefault();
  const fcOn=typeof isChartForecastOn==="function" && isChartForecastOn(AI_CHAT_FC_SCOPE);
  const samples=spec.samples||[];
  const series=normalizeAIChatSeries(spec.series);
  const yMin=spec.yMin!=null?Number(spec.yMin):null;
  const yMax=spec.yMax!=null?Number(spec.yMax):null;
  const title=spec.title||"";
  const hasServerFC=series.some(s=>s.kind==="forecast"||s.dashed);
  const paintSeries=aiChatSeriesForToggle(series, fcOn);
  let horizonSec=Number(spec.horizonSec)||0;
  if(!horizonSec && samples.length>=2){
    const a=_fcSampleTs?_fcSampleTs(samples[0]):(samples[0].timestamp||0);
    const b=_fcSampleTs?_fcSampleTs(samples[samples.length-1]):(samples[samples.length-1].timestamp||0);
    horizonSec=Math.max(0, Math.round(b-a));
  }
  if(fcOn && horizonSec>0 && horizonSec<1800) horizonSec=1800;
  const opts={
    title, cssH:200, legendMode:fcOn?"wrap":"dash", noEntrance:true,
    nowTs:fcOn?(spec.nowTs||0):0,
    forecastScope:AI_CHAT_FC_SCOPE,
    horizonSec: fcOn?horizonSec:0,
    reload: spec.reload||null,
    _fcBase: { samples, series, yMin, yMax, title, nowTs:spec.nowTs||0, horizonSec, reload: spec.reload||null }
  };
  let ch=null;
  let fcMeta=null;
  try{
    if(fcOn && !hasServerFC && samples.length>=4 && typeof createChartWithForecast==="function"){
      ch=await createChartWithForecast(cid, samples, paintSeries, yMin, yMax,
        Object.assign({}, opts, { forecast:true, forecastScope:AI_CHAT_FC_SCOPE }));
      if(ch) fcMeta=ch._fcMeta||null;
    }
    if(!ch){
      ch=createChart(cid, samples, paintSeries.length?paintSeries:series, yMin, yMax, opts);
    }
  }catch(_){ ch=null; }
  if(!ch){
    try{ ch=createChart(cid, samples, series, yMin, yMax, Object.assign({}, opts, { nowTs:0 })); }catch(__){}
  }
  if(ch){
    const reload=spec.reload||null;
    ch._aiBase={ samples, series, yMin, yMax, title, nowTs:spec.nowTs||0, horizonSec, reload };
    ch.reload=reload;
    ch.forecastScope=AI_CHAT_FC_SCOPE;
    if(ch._fcBase) ch._fcBase.reload=reload;
    AI_CHAT_CHARTS[cid]=ch;
  }
  // 在卡片头展示预测状态（失败时提示，避免开关开着却无虚线）
  const card=canvas.closest(".ai-chart-card");
  if(card){
    let hint=card.querySelector(".ai-chart-fc-hint");
    if(fcOn){
      const ok=fcMeta && (fcMeta.ok===true || fcMeta.OK===true);
      const hasFCLine=(ch && ch.series||[]).some(s=>s && (s.kind==="forecast"||s.dashed));
      const msg=ok||hasFCLine
        ? (fcMeta && (fcMeta.message||fcMeta.Message) || "左=历史 · 右=预测（虚线）")
        : ((fcMeta && (fcMeta.message||fcMeta.Message)) || (samples.length<4?"采样不足，暂无法预测":"预测未生成，请稍后重试或放大预览"));
      if(!hint){
        hint=document.createElement("div");
        hint.className="ai-chart-fc-hint";
        const head=card.querySelector(".ai-chart-head");
        if(head && head.nextSibling) card.insertBefore(hint, head.nextSibling);
        else card.appendChild(hint);
      }
      hint.textContent=msg;
      hint.classList.toggle("warn", !(ok||hasFCLine));
    } else if(hint){
      hint.remove();
    }
  }
  canvas.dataset.chartBound="1";
  return ch;
}

async function bindAIChatWidgets(root,actions){
  if(!root||!actions||!actions.length||typeof createChart!=="function") return;
  ensureAIChatForecastDefault();
  // 刷新工具条 chip 状态（会话内多图共享 scope）
  root.querySelectorAll(`[data-chart-forecast="${AI_CHAT_FC_SCOPE}"]`).forEach(btn=>{
    if(typeof isChartForecastOn==="function" && isChartForecastOn(AI_CHAT_FC_SCOPE)) btn.classList.add("active");
    else btn.classList.remove("active");
  });
  for(const a of actions){
    if(!a||a.type!=="show_chart"||!a.chart) continue;
    const cid="aiChart_"+(a.id||aiChatActionKey(a).replace(/\|/g,"_"));
    const canvas=$(cid) || root.querySelector("#"+CSS.escape(cid));
    if(!canvas) continue;
    const samples=Array.isArray(a.chart.samples)?a.chart.samples:[];
    const series=normalizeAIChatSeries(a.chart.series);
    if(!samples.length||!series.length){
      const card=canvas.closest(".ai-chart-card");
      if(card&&!card.querySelector(".ai-chart-empty")){
        const wrap=card.querySelector(".ai-chart-wrap");
        if(wrap) wrap.innerHTML=`<div class="ai-chart-empty">${esc(I18N.t("sre.chart_empty","图表数据不可用或已过期，请重试生成"))}</div>`;
      }
      continue;
    }
    const src=a.source||{};
    const hostId=String(src.host_id||src.hostId||"").trim();
    const metrics=String(src.metrics||"").split(",").map(s=>s.trim()).filter(Boolean);
    const reload=hostId?{
      hostId,
      mode: metrics.length ? "ai-mapped" : "fields",
      metrics,
      forecastScope: AI_CHAT_FC_SCOPE
    }:null;
    const spec={
      samples, series,
      yMin:a.chart.y_min!=null?Number(a.chart.y_min):null,
      yMax:a.chart.y_max!=null?Number(a.chart.y_max):null,
      title:a.title||a.label||"",
      nowTs:a.chart.now_ts||0,
      horizonSec:a.chart.horizon_sec||0,
      reload
    };
    AI_CHAT_CHART_SPECS[cid]=spec;
    if(canvas.dataset.chartBound==="1" && AI_CHAT_CHARTS[cid]) continue;
    try{
      await paintAIChatChart(cid, spec);
    }catch(e){
      const card=canvas.closest(".ai-chart-card");
      if(card){
        const wrap=card.querySelector(".ai-chart-wrap")||card;
        const err=document.createElement("div");
        err.className="ai-chart-empty";
        err.textContent=I18N.t("sre.chart_paint_fail","图表渲染失败，请重试");
        wrap.appendChild(err);
      }
    }
  }
}

async function rebindAIChatChartsForecast(){
  ensureAIChatForecastDefault();
  const ids=Object.keys(AI_CHAT_CHART_SPECS||{});
  for(const cid of ids){
    const canvas=document.getElementById(cid);
    if(!canvas || !document.body.contains(canvas)) continue;
    canvas.dataset.chartBound="";
    await paintAIChatChart(cid, AI_CHAT_CHART_SPECS[cid]);
  }
  document.querySelectorAll(`[data-chart-forecast="${AI_CHAT_FC_SCOPE}"]`).forEach(btn=>{
    if(typeof isChartForecastOn==="function" && isChartForecastOn(AI_CHAT_FC_SCOPE)) btn.classList.add("active");
    else btn.classList.remove("active");
  });
}

document.addEventListener("click",(e)=>{
  const en=e.target&&e.target.closest&&e.target.closest("[data-ai-chart-zoom]");
  if(!en) return;
  e.preventDefault();
  const cid=en.getAttribute("data-ai-chart-zoom");
  if(!cid) return;
  const open=async()=>{
    let ch=AI_CHAT_CHARTS[cid];
    const spec=AI_CHAT_CHART_SPECS[cid];
    if((!ch||!ch.all)&&spec){
      const canvas=document.getElementById(cid);
      if(canvas) canvas.dataset.chartBound="";
      ch=await paintAIChatChart(cid, spec);
    }
    if(ch && typeof openChartZoom==="function") openChartZoom(ch);
  };
  open().catch(()=>{});
});
async function handleAIChatAction(a){
  if(!a||!a.type) return;
  if(a.type==="open_dashboard"&&a.id){
    try{
      if(typeof switchView==="function") switchView("dashboards");
      if(typeof openDashboard==="function") await openDashboard(a.id);
      const mask=$("aiChatMask"); if(mask) mask.classList.remove("show");
      if(typeof toast==="function") toast(I18N.t("sre.opened_dashboard","已打开看板"),"ok");
    }catch(e){ if(typeof toast==="function") toast(String(e),"err"); }
    return;
  }
  if(a.type==="navigate_view"&&a.view){
    try{
      const view=String(a.view);
      // Client soft allowlist mirroring server uiViewCatalog (unknown views ignored).
      const known=typeof PAGE_META==="object"&&PAGE_META?Object.keys(PAGE_META):null;
      if(known&&known.length&&!known.includes(view)&&typeof switchView==="function"){
        // still try switchView — PAGE_META may use different keys; block obvious path injection
        if(/[\/\\]/.test(view)||view.length>64){ if(typeof toast==="function") toast(I18N.t("sre.invalid_view","非法视图"),"err"); return; }
      }
      const mask=$("aiChatMask"); if(mask) mask.classList.remove("show");
      if(typeof switchView==="function") switchView(view);
      const title=a.title||a.label||a.view;
      if(typeof toast==="function") toast(I18N.t("sre.opened_view","已打开界面")+" · "+title,"ok");
    }catch(e){ if(typeof toast==="function") toast(String(e),"err"); }
    return;
  }
  if(a.type==="export_report"){
    await exportAIChatReport(a.title||"AI 分析报告", a.body||"");
    return;
  }
  if(a.type==="drill_down"){
    const target=a.target||"";
    if(target==="host_detail"&&a.host_id){
      try{
        const mask=$("aiChatMask"); if(mask) mask.classList.remove("show");
        if(typeof switchView==="function") switchView("hosts");
        if(typeof openDetail==="function") await openDetail(a.host_id, a.host_name||a.host_id);
        if(typeof toast==="function") toast(I18N.t("sre.opened_host","已打开主机详情"),"ok");
      }catch(e){ if(typeof toast==="function") toast(String(e),"err"); }
      return;
    }
    if(target==="dashboard"&&(a.dashboard_id||a.id)){
      return handleAIChatAction({type:"open_dashboard",id:a.dashboard_id||a.id,label:a.label});
    }
    if(target==="view"&&a.view){
      return handleAIChatAction({type:"navigate_view",view:a.view,label:a.label,title:a.title});
    }
    if(target==="prompt"&&a.prompt){
      const inp=$("aiChatInput"); if(inp){ inp.value=String(a.prompt); autoGrowAIInput({target:inp}); }
      sendAIChat();
      return;
    }
    if(a.prompt){
      const inp=$("aiChatInput"); if(inp){ inp.value=String(a.prompt); autoGrowAIInput({target:inp}); }
      sendAIChat();
    }
  }
}
function bindAIChatActions(root,actions){
  if(!root) return;
  const clickable=(actions||[]).filter(a=>a&&a.type&&a.type!=="show_chart"&&a.type!=="show_stat"&&a.type!=="show_table"&&a.type!=="show_logs");
  root.querySelectorAll("[data-ai-act]").forEach(btn=>{
    btn.onclick=async()=>{
      const a=clickable[Number(btn.dataset.aiAct)];
      if(!a) return;
      await handleAIChatAction(a);
    };
  });
  bindAIDashLinks(root);
  bindAIChatWidgets(root, actions||[]);
}
function bindAIDashLinks(root){
  if(!root) return;
  root.querySelectorAll("a.ai-dash-link[data-dash]").forEach(a=>{
    if(a.dataset.bound) return;
    a.dataset.bound="1";
    a.addEventListener("click",async(ev)=>{
      ev.preventDefault();
      const id=a.getAttribute("data-dash");
      if(!id) return;
      try{
        if(typeof switchView==="function") switchView("dashboards");
        if(typeof openDashboard==="function") await openDashboard(id);
        const mask=$("aiChatMask"); if(mask) mask.classList.remove("show");
        if(typeof toast==="function") toast(I18N.t("sre.opened_dashboard","已打开看板"),"ok");
      }catch(e){ if(typeof toast==="function") toast(String(e),"err"); }
    });
  });
}
async function exportAIChatReport(title, body, fmt, opts){
  if(typeof exportModel!=="function"){ if(typeof toast==="function") toast("导出组件不可用","err"); return; }
  const format=fmt||await pickAIExportFormat();
  if(!format) return;
  opts=opts||{};
  const actions=parseAIChatActions(opts.actions);
  const root=opts.root||null;
  const sections=typeof parseAssistMarkdownTables==="function"?parseAssistMarkdownTables(body||""):[];
  const figures=[];
  if(root){
    root.querySelectorAll(".ai-chart-card").forEach(card=>{
      const t=(card.querySelector(".ai-chart-title")||{}).textContent||"趋势图";
      const canvas=card.querySelector("canvas");
      if(!canvas) return;
      try{
        const dataUrl=canvas.toDataURL("image/png");
        if(dataUrl&&dataUrl.length>64) figures.push({title:t,dataUrl});
      }catch(e){ /* canvas tainted / empty */ }
    });
  }
  // 无画布时，用 actions 里的表格/日志/采样点兜底，保证导出不丢结构化结果
  for(const a of actions){
    if(!a||!a.type) continue;
    if(a.type==="show_table"&&Array.isArray(a.columns)&&Array.isArray(a.rows)){
      sections.push({
        title:a.title||a.label||"表格",
        columns:a.columns.map(String),
        rows:a.rows.map(r=>a.columns.map(c=>r&&r[c]!=null?String(r[c]):""))
      });
    } else if(a.type==="show_logs"&&Array.isArray(a.lines)&&a.lines.length){
      sections.push({
        title:a.title||a.label||"日志",
        columns:["时间","内容"],
        rows:a.lines.slice(0,200).map(ln=>{
          if(ln&&typeof ln==="object") return [String(ln.ts||""),String(ln.line!=null?ln.line:ln)];
          return ["",String(ln)];
        })
      });
    } else if(a.type==="show_stat"){
      sections.push({
        title:a.title||a.label||"指标",
        columns:["指标","值","单位"],
        rows:[[a.title||a.label||"指标", a.value!=null?String(a.value):"—", a.unit||""]]
      });
    } else if(a.type==="show_chart"&&a.chart&&Array.isArray(a.chart.samples)&&a.chart.samples.length){
      const series=Array.isArray(a.chart.series)?a.chart.series:[];
      const cols=["时间"].concat(series.map(s=>s.label||s.key||"系列"));
      const rows=a.chart.samples.slice(0,500).map(row=>{
        const ts=row&&(row.timestamp!=null?row.timestamp:row.ts);
        const tstr=ts!=null?new Date(Number(ts)* (Number(ts)>1e12?1:1000)).toLocaleString():"";
        return [tstr].concat(series.map(s=>{
          const v=row?row[s.key]:null;
          return v==null||v===""?"":String(v);
        }));
      });
      sections.push({title:a.title||a.label||"趋势数据",columns:cols,rows});
    }
  }
  const visualPanels=figures.map((f,i)=>({title:f.title,dataUrl:f.dataUrl,x:0,y:i*9,w:24,h:8}));
  const model={
    title: title||I18N.t("sre.chat_export_title","AI 对话报告"),
    subtitle:"AIOps · "+new Date().toLocaleString(),
    summaryTitle:"报告信息",
    meta:[["来源","AI 对话"],["生成时间",new Date().toLocaleString()],["格式",format],["图表数",String(figures.length)]],
    narrativeTitle:"AI 分析与建议",
    narrative:body||"",
    sections,
    figures,
    visualPanels,
    footer:"AI 结果仅作为运维决策辅助；高风险操作须经人工验证与审批。"
  };
  // PDF/Word：有图表截图时走视觉成品路径，正文写入副标题区；其它格式保留叙事 + 数据表
  if((format==="pdf"||format==="word")&&visualPanels.length){
    model.kind="visual";
    model.subtitle=(body||"").slice(0,280)+(body&&body.length>280?"…":"");
  }
  try{
    const ok=await exportModel(model,format,title||"AI对话报告");
    if(ok===false&&typeof toast==="function") toast(I18N.t("assist.popup_blocked","浏览器拦截了导出窗口，请允许弹窗后重试"),"warn");
    else if(typeof toast==="function") toast(I18N.t("sre.exported","已导出"),"ok");
  }catch(e){ if(typeof toast==="function") toast(String(e),"err"); }
}
function pickAIExportFormat(){
  return new Promise(resolve=>{
    const formats=[
      {id:"markdown",label:"Markdown (.md)"},
      {id:"html",label:"HTML"},
      {id:"word",label:"Word (.docx)"},
      {id:"pdf",label:"PDF"},
      {id:"excel",label:"Excel (.xlsx)"}
    ];
    const wrap=document.createElement("div");
    wrap.className="mask show";
    wrap.style.zIndex="13000";
    wrap.innerHTML=`<div class="modal" style="max-width:420px;width:90vw"><div class="modal-head"><h3>${I18N.t("sre.export_format","选择导出格式")}</h3><button class="btn ghost close" type="button">✕</button></div><div class="modal-body" style="display:flex;flex-direction:column;gap:8px">${formats.map(f=>`<button type="button" class="btn" data-fmt="${f.id}" style="justify-content:flex-start">${f.label}</button>`).join("")}</div></div>`;
    const done=(v)=>{ try{wrap.remove();}catch(e){} resolve(v); };
    wrap.querySelector(".close").onclick=()=>done(null);
    wrap.addEventListener("click",e=>{ if(e.target===wrap) done(null); });
    wrap.querySelectorAll("[data-fmt]").forEach(b=>b.onclick=()=>done(b.getAttribute("data-fmt")));
    document.body.appendChild(wrap);
  });
}
function openAIChat(){
  newAIChat();
  $("aiChatMask").classList.add("show");
  loadAISessions();
  setTimeout(()=>{ const i=$("aiChatInput"); if(i) i.focus(); },80);
}
// 开新会话：清空会话 id / 历史 / 消息区
function newAIChat(){
  if(_aiChatBusy) stopAIChat(); // 开新会话前终止在途
  AI_CHAT_SESSION=0; AI_CHAT_HISTORY=[]; AI_ATTACHMENTS=[]; AI_CHAT_QUEUE=[];
  const log=$("aiChatLog"); if(log) log.innerHTML=AI_CHAT_INTRO;
  const sel=$("aiSessionSelect"); if(sel) sel.value="";
  renderAttachments(); renderQueueHint(); setAIChatBusyUI(false);
  loadAISuggestions(); // 拉取并渲染快捷问题/推荐 Prompt
}
// ===== 快捷问题 / 推荐 Prompt（结合当前告警/主机/日志的动态建议 + 能力示例，随机展示） =====
let AI_SUGGEST={dynamic:[],curated:[]};
function _aiShuffle(a){ a=a.slice(); for(let i=a.length-1;i>0;i--){ const j=Math.floor(Math.random()*(i+1)); const t=a[i]; a[i]=a[j]; a[j]=t; } return a; }
async function loadAISuggestions(){
  const box=$("aiChatSuggest"); if(!box) return;
  try{
    const r=await fetch(`${API}/hermes/suggestions`); if(!r.ok){ box.style.display="none"; return; }
    AI_SUGGEST=(await r.json())||{dynamic:[],curated:[]};
    renderAISuggest();
  }catch(e){ box.style.display="none"; }
}
function renderAISuggest(){
  const box=$("aiChatSuggest"); if(!box) return;
  const dyn=(AI_SUGGEST.dynamic||[]).slice(0,2);
  const need=Math.max(0,5-dyn.length);
  const cur=_aiShuffle(AI_SUGGEST.curated||[]).slice(0,need);
  const items=dyn.concat(cur);
  if(!items.length){ box.style.display="none"; return; }
  box.style.display="";
  box.innerHTML=`<div class="ai-suggest-head"><span>💡 ${I18N.t("sre.try_questions","试试这些问题")}</span><button class="ai-suggest-refresh" title="${I18N.t("sre.refresh_suggest_title","换一批推荐")}">↻ ${I18N.t("sre.refresh_batch","换一批")}</button></div>`+
    `<div class="ai-suggest-chips">`+items.map(q=>`<button class="ai-suggest-chip" data-q="${esc(q)}">${esc(q)}</button>`).join("")+`</div>`;
  const rf=box.querySelector(".ai-suggest-refresh"); if(rf) rf.onclick=renderAISuggest;
  box.querySelectorAll(".ai-suggest-chip").forEach(b=>b.onclick=()=>{ const inp=$("aiChatInput"); if(inp) inp.value=b.dataset.q; sendAIChat(); });
}
// 加载历史会话列表到下拉选择器
async function loadAISessions(){
  const sel=$("aiSessionSelect"); if(!sel) return;
  try{
    const r=await fetch(`${API}/hermes/sessions`);
    if(!r.ok) return;
    const list=await r.json();
    sel.innerHTML=`<option value="">＋ ${I18N.t("sre.new_session","新会话")}</option>`+
      (Array.isArray(list)?list:[]).map(s=>{
        const cnt=s.msg_count?` (${s.msg_count})`:"";
        return `<option value="${s.id}">${esc((s.title||I18N.t("sre.session","会话"))+cnt)}</option>`;
      }).join("");
    sel.value=AI_CHAT_SESSION?String(AI_CHAT_SESSION):"";
  }catch(e){ /* 无 PG / 接口不可用时静默 */ }
}
// 切换到某历史会话并恢复其消息（含图表/组件 actions，永久可回看）
async function switchAISession(id){
  if(!id){ newAIChat(); return; }
  try{
    const r=await fetch(`${API}/hermes/sessions/${id}`);
    if(!r.ok) throw new Error("HTTP "+r.status);
    const j=await r.json();
    const msgs=(j.messages||[]).filter(m=>m&&(m.role==="user"||m.role==="assistant"));
    AI_CHAT_SESSION=Number(id);
    AI_CHAT_HISTORY=msgs.map(m=>({role:m.role,content:m.content,actions:parseAIChatActions(m.actions)}));
    const log=$("aiChatLog");
    if(log){
      log.innerHTML=msgs.length
        ? msgs.map(m=>{
            if(m.role==="user") return `<div class="ai-chat-msg me">${esc(m.content||"")}</div>`;
            const acts=parseAIChatActions(m.actions);
            return `<div class="ai-chat-msg ai">${renderAIMarkdown(filterDisplayContent(m.content||""))}${renderAIChatWidgets(acts)}${renderAIChatActions(acts)}</div>`;
          }).join("")
        : `<div class="ai-chat-msg sys">${I18N.t("sre.empty_session","（空会话）")}</div>`;
      const aiNodes=log.querySelectorAll(".ai-chat-msg.ai");
      let aiIdx=0;
      msgs.forEach(m=>{
        if(m.role!=="assistant") return;
        const d=aiNodes[aiIdx++]; if(!d) return;
        const acts=parseAIChatActions(m.actions);
        d._aiActions=acts;
        addCopyTool(d,d.innerText||m.content||"",{actions:acts});
        bindAIChatActions(d,acts);
      });
      log.scrollTop=log.scrollHeight;
      requestAnimationFrame(()=>{
        aiNodes.forEach((d,i)=>{
          // re-bind charts after layout
          const m=msgs.filter(x=>x.role==="assistant")[i];
          if(m) bindAIChatWidgets(d, parseAIChatActions(m.actions));
        });
      });
    }
  }catch(e){ if(typeof toast==="function") toast(I18N.t("sre.load_session_failed","加载会话失败")+"："+e,"err"); }
}
// 会话有更新后延迟刷新列表（合并短时间内多次更新）
let _aiSessTimer=null;
function refreshAISessionsSoon(){ if(_aiSessTimer) clearTimeout(_aiSessTimer); _aiSessTimer=setTimeout(loadAISessions,700); }
function appendChatMsg(role,text){
  const log=$("aiChatLog"); if(!log) return null;
  const div=document.createElement("div");
  div.className="ai-chat-msg "+(role==="user"?"me":role==="assistant"?"ai":"sys");
  div.textContent=text;
  log.appendChild(div); log.scrollTop=log.scrollHeight;
  return div;
}
let _aiChatBusy=false;
let _aiChatAbort=null;    // 当前请求的 AbortController
let _aiChatAborted=false; // 本次是否被用户终止
let AI_CHAT_QUEUE=[];     // 排队消息 {msg, atts}
let AI_ATTACHMENTS=[];    // 待发送附件：{kind:"image"|"file", name, mime, data(图片base64), text(文件文本)}
function setAIChatBusyUI(busy){
  const send=$("aiChatSendBtn"), stop=$("aiChatStopBtn");
  if(send) send.style.display=busy?"none":"";
  if(stop) stop.style.display=busy?"":"none";
  const log=$("aiChatLog"); if(log) log.classList.toggle("ai-streaming", busy); // 流式打字光标
}
// 输入框自增高（Claude 风：随内容增长，封顶 168px 后内部滚动）
function autoGrowAIInput(){ const t=$("aiChatInput"); if(!t) return; t.style.height="auto"; t.style.height=Math.min(t.scrollHeight,168)+"px"; }
function renderQueueHint(){
  const el=$("aiChatQueue"); if(!el) return;
  el.textContent=AI_CHAT_QUEUE.length?`⏳ ${I18N.t("sre.queued","已排队")} ${AI_CHAT_QUEUE.length} ${I18N.t("sre.queued_suffix","条，将在当前回复完成后依次发送")}`:"";
}
async function sendAIChat(){
  const inp=$("aiChatInput"); if(!inp) return;
  let msg=inp.value.trim();
  if(window._AI_WRITE_APPROVAL && window._AI_WRITE_APPROVAL.id){
    const ap=window._AI_WRITE_APPROVAL;
    msg=(msg?msg+"\n\n":"")+`【写工具审批】调用写工具 ${ap.tool} 时请使用 approval_id=${ap.id}`+(ap.args_hash?`（建议 args_hash=${ap.args_hash}）`:"");
    window._AI_WRITE_APPROVAL=null;
    const hint=$("aiWriteApprovalHint"); if(hint) hint.style.display="none";
  }
  const atts=AI_ATTACHMENTS.slice();
  if(!msg && !atts.length) return; // 无文本且无附件则不发
  { const _sg=$("aiChatSuggest"); if(_sg) _sg.style.display="none"; } // 发起对话后隐藏推荐问题
  if(_aiChatBusy){ // 忙时排队：完成后自动续发（可点终止清空排队）
    AI_CHAT_QUEUE.push({msg,atts});
    inp.value=""; AI_ATTACHMENTS=[]; renderAttachments(); renderQueueHint();
    return;
  }
  inp.value=""; autoGrowAIInput();
  _aiChatBusy=true; _aiChatAborted=false; setAIChatBusyUI(true);
  _aiChatAbort=(typeof AbortController!=="undefined")?new AbortController():null;
  const imgN=atts.filter(a=>a.kind==="image").length, fileN=atts.filter(a=>a.kind==="file").length;
  const attNote=atts.length?`　<span class="ai-att-note">📎 ${imgN?imgN+" "+I18N.t("sre.unit_images","图")+" ":""}${fileN?fileN+" "+I18N.t("sre.unit_files","文件"):""}</span>`:"";
  const log=$("aiChatLog");
  if(log){ const d=document.createElement("div"); d.className="ai-chat-msg me"; d.innerHTML=esc(msg||I18N.t("sre.attachment_only","（附件）"))+attNote; log.appendChild(d); log.scrollTop=log.scrollHeight; }
  AI_CHAT_HISTORY.push({role:"user",content:msg||I18N.t("sre.attachment_only","（附件）")});
  AI_ATTACHMENTS=[]; renderAttachments();
  const pending=appendChatMsg("assistant","");
  if(pending) pending.innerHTML='<div class="ai-thinking"><span class="ai-thinking-dots"><span></span><span></span><span></span></span> <span class="ai-thinking-text">'+I18N.t("sre.thinking","正在思考…")+'</span></div>';
  let answer="";
  let lastRunId="";
  const uiActions=[];
  try{
    const images=atts.filter(a=>a.kind==="image").map(a=>({mime:a.mime,data:a.data}));
    const files=atts.filter(a=>a.kind==="file").map(a=>({name:a.name,text:a.text}));
    const r=await fetch(`${API}/hermes/chat`,{method:"POST",headers:{"Content-Type":"application/json"},
      signal:_aiChatAbort?_aiChatAbort.signal:undefined,
      body:JSON.stringify({message:msg,session_id:AI_CHAT_SESSION,history:historyForAIChatAPI(AI_CHAT_HISTORY.slice(0,-1)),images,files,stream:true})});
    if(!r.ok){ throw new Error("HTTP "+r.status); }
    let streamed=false;
    let reasoning=""; // 推理模型思维链（独立于 answer，渲染到「思考过程」折叠区）
    // 工具调用状态 chip（run→ok/err）：与回答正文分离渲染，实时更新且不污染最终回答
    const toolStates=[];
    const toolTraceHTML=()=> toolStates.length ? '<div class="ai-tool-trace">'+toolStates.map(s=>{
      const ic = s.state==="run"
        ? '<svg class="ai-tool-spin" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>'
        : (s.state==="ok" ? "✓" : "✗");
      return `<span class="ai-tool-chip ${s.state}">${ic}<span>${esc(s.name)}</span></span>`;
    }).join("")+'</div>' : "";
    // 流式渲染：使用 requestAnimationFrame 同步到显示刷新（≈16ms），消除 setTimeout 攒批延迟
    let streamRAF=null;
    const paintStream=()=>{
      if(!pending) return;
      const hasWidgets=(uiActions||[]).some(a=>a&&(a.type==="show_chart"||a.type==="show_stat"||a.type==="show_table"||a.type==="show_logs"));
      pending.innerHTML=renderReasoningBlock(reasoning,true)+toolTraceHTML()
        +'<div class="ai-stream-body"><span class="ai-stream-text">'+esc(answer||"")+"</span><span class=\"ai-stream-cursor\">0</span></div>"
        +(hasWidgets?renderAIChatWidgets(uiActions):"")
        +renderAIChatActions(uiActions);
      bindAIChatActions(pending,uiActions);
      if(hasWidgets) requestAnimationFrame(()=>bindAIChatWidgets(pending,uiActions));
    };
    const schedulePaint=()=>{
      if(streamRAF) return;
      streamRAF=requestAnimationFrame(()=>{ streamRAF=null; paintStream(); });
    };
    const paintFinal=()=>{
      if(streamRAF){ cancelAnimationFrame(streamRAF); streamRAF=null; }
      if(pending){
        pending.innerHTML=renderReasoningBlock(reasoning,false)+toolTraceHTML()
          +(renderAIMarkdown(answer)||(toolStates.length?"":"…"))
          +renderAIChatWidgets(uiActions)
          +renderAIChatActions(uiActions);
        pending._aiActions=uiActions.slice();
        bindAIChatActions(pending,uiActions);
        bindAIDashLinks(pending);
        requestAnimationFrame(()=>bindAIChatWidgets(pending,uiActions));
      }
    };
    await readSSEStream(r,
      (delta,fullText)=>{
        const stick=aiChatStick();
        if(!streamed){ streamed=true; }
        answer=filterDisplayContent(fullText);
        schedulePaint();
        if(stick) aiChatToBottom();
      },
      (err)=>{ if(streamRAF){ cancelAnimationFrame(streamRAF); streamRAF=null; } if(pending){ pending.textContent="✗ "+err; pending.classList.add("err"); } if(/AI 未配置|未启用/.test(String(err||""))) promptOpenAIConfig(err); },
      (fullText)=>{
        if(pending){
          answer=filterDisplayContent(fullText||answer||"");
          paintFinal();
        }
        aiChatToBottom();
      },
      null,
      (meta)=>{
        if(meta&&meta.session_id){ AI_CHAT_SESSION=Number(meta.session_id); }
        if(meta&&(meta.run_id||meta.assist_id)){ lastRunId=String(meta.run_id||meta.assist_id); }
        applyRAGMetaHint(meta, "aiChatLog");
      },
      (t)=>{ // 工具状态帧：run 追加 chip，ok/err 更新最近的同名 run chip
        if(!t||!t.name) return;
        if(t.state==="run") toolStates.push({name:t.name,state:"run"});
        else { for(let i=toolStates.length-1;i>=0;i--){ if(toolStates[i].name===t.name&&toolStates[i].state==="run"){ toolStates[i].state=t.state; break; } } }
        if(pending && !streamed){ streamed=true; }
        schedulePaint();
        if(aiChatStick()) aiChatToBottom();
      },
      (rd,fullReasoning)=>{ // 思维链增量：累积并实时渲染到折叠区
        if(!streamed){ streamed=true; }
        reasoning=fullReasoning;
        schedulePaint();
        if(aiChatStick()) aiChatToBottom();
      },
      {
        onAction:(act)=>{
          if(!act||!act.type) return;
          uiActions.push(act);
          if(pending && !streamed){ streamed=true; }
          schedulePaint();
          if(aiChatStick()) aiChatToBottom();
        }
      }
    );
    if(answer){
      AI_CHAT_HISTORY.push({role:"assistant",content:answer,actions:uiActions.slice()});
      if(pending&&lastRunId) pending.dataset.runId=lastRunId;
      if(pending) pending._aiActions=uiActions.slice();
      addCopyTool(pending,answer,{runId:lastRunId,actions:uiActions.slice()});
    }
    refreshAISessionsSoon();
  }catch(e){
    if(_aiChatAborted || (e&&e.name==="AbortError")){ if(pending){ pending.textContent="⏹ "+I18N.t("sre.aborted","已终止"); pending.className="ai-chat-msg sys"; } }
    else {
      const msg=String(e);
      if(pending){ pending.textContent="✗ "+I18N.t("sre.request_failed","请求失败")+"："+msg; pending.classList.add("err"); }
      if(/AI 未配置|未启用/.test(msg)) promptOpenAIConfig(msg);
    }
  }
  finally{
    _aiChatBusy=false; _aiChatAbort=null; setAIChatBusyUI(false);
    if(inp) inp.focus();
    if(!_aiChatAborted && AI_CHAT_QUEUE.length){ // 处理排队（终止时不自动续发）
      const next=AI_CHAT_QUEUE.shift(); renderQueueHint();
      const i=$("aiChatInput"); if(i) i.value=next.msg||"";
      AI_ATTACHMENTS=next.atts||[]; renderAttachments();
      setTimeout(sendAIChat,80);
    }
  }
}
// 终止：立即中止在途请求（后端 ctx 取消随即停止 LLM 调用与工具执行），并清空排队
function stopAIChat(){
  _aiChatAborted=true;
  if(_aiChatAbort){ try{ _aiChatAbort.abort(); }catch(e){} }
  AI_CHAT_QUEUE=[]; renderQueueHint();
}
// 撤销上一轮问答：移除末尾 user+assistant 气泡 + 本地历史 + 服务端会话截断，并回填到输入框
async function undoAIChat(){
  if(_aiChatBusy){ if(typeof toast==="function") toast(I18N.t("sre.gen_stop_first_undo","生成中，请先终止再撤销"),"err"); return; }
  const log=$("aiChatLog"); if(!log) return;
  let lastUser="";
  for(let i=AI_CHAT_HISTORY.length-1;i>=0;i--){ if(AI_CHAT_HISTORY[i].role==="user"){ lastUser=AI_CHAT_HISTORY[i].content; break; } }
  if(AI_CHAT_HISTORY.length && AI_CHAT_HISTORY[AI_CHAT_HISTORY.length-1].role==="assistant") AI_CHAT_HISTORY.pop();
  if(AI_CHAT_HISTORY.length && AI_CHAT_HISTORY[AI_CHAT_HISTORY.length-1].role==="user") AI_CHAT_HISTORY.pop();
  const bubbles=()=>Array.from(log.querySelectorAll(".ai-chat-msg")).filter(b=>!b.classList.contains("sys"));
  if(!bubbles().length){ if(typeof toast==="function") toast(I18N.t("sre.no_undo","没有可撤销的对话"),"err"); return; }
  const lastAi=[...bubbles()].reverse().find(b=>b.classList.contains("ai")); if(lastAi) lastAi.remove();
  const lastMe=[...bubbles()].reverse().find(b=>b.classList.contains("me")); if(lastMe) lastMe.remove();
  if(AI_CHAT_SESSION){ try{ await fetch(`${API}/hermes/sessions/${AI_CHAT_SESSION}/undo`,{method:"POST"}); }catch(e){} refreshAISessionsSoon(); }
  const inp=$("aiChatInput"); if(inp&&lastUser){ inp.value=lastUser; inp.focus(); }
}
function copyText(t){
  if(navigator.clipboard&&navigator.clipboard.writeText){ return navigator.clipboard.writeText(t).catch(()=>_fallbackCopy(t)); }
  _fallbackCopy(t);
}
function _fallbackCopy(t){ const ta=document.createElement("textarea"); ta.value=t; ta.style.position="fixed"; ta.style.opacity="0"; document.body.appendChild(ta); ta.select(); try{document.execCommand("copy");}catch(e){} ta.remove(); }
// 给一条 AI 回复挂上图标化操作栏（朗读 / 复制 / 导出 / 重答 / 👍👎），低频文案全部收进 title
function addCopyTool(div,rawText,opts){
  if(!div) return;
  opts=opts||{};
  const actions=parseAIChatActions(opts.actions!=null?opts.actions:div._aiActions);
  div._aiActions=actions;
  // 代码块独立复制（复制对应 <pre> 内容）
  div.querySelectorAll(".ai-code-copy").forEach(b=>{
    b.onclick=()=>{ const w=b.closest(".ai-code-wrap"); const c=w&&w.querySelector("pre code"); if(c){ copyText(c.textContent); b.textContent=I18N.t("sre.copied","已复制"); setTimeout(()=>b.textContent=I18N.t("sre.copy","复制"),1200); } };
  });
  const existing=div.querySelector(".ai-msg-tools"); if(existing) existing.remove();
  const bar=document.createElement("div"); bar.className="ai-msg-tools";
  const iconBtn=(cls,title,svg,onClick)=>{
    const b=document.createElement("button");
    b.type="button"; b.className=cls||""; b.title=title; b.setAttribute("aria-label",title);
    b.innerHTML=svg; b.onclick=onClick; return b;
  };
  const ico={
    speak:'<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 5L6 9H2v6h4l5 4V5z"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M18.5 5.5a9 9 0 0 1 0 13"/></svg>',
    copy:'<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>',
    export:'<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>',
    regen:'<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>'
  };
  bar.appendChild(iconBtn("ai-speak-btn",I18N.t("sre.speak_title","朗读本条回复"),ico.speak,()=>speakAIText(rawText, bar.querySelector(".ai-speak-btn"))));
  bar.appendChild(iconBtn("",I18N.t("sre.copy_reply","复制回复"),ico.copy,e=>{
    copyText(rawText);
    const b=e.currentTarget; b.classList.add("ok"); setTimeout(()=>b.classList.remove("ok"),900);
  }));
  if(opts.export!==false && (rawText || actions.length) && typeof exportModel==="function"){
    bar.appendChild(iconBtn("",I18N.t("sre.export_reply","导出本条回复（含图表）"),ico.export,
      ()=>exportAIChatReport(I18N.t("sre.chat_export_title","AI 对话报告"), rawText||"", null, {actions,root:div})));
  }
  if(opts.regenerate!==false){
    bar.appendChild(iconBtn("",I18N.t("sre.regen_title","用上一条问题重新回答"),ico.regen,regenerateAIChat));
  }
  if(opts.feedback===false){ div.appendChild(bar); return; }
  // 无 run_id 时仍展示按钮，但提交时提示（避免假反馈污染）
  const up=document.createElement("button"); up.type="button"; up.textContent="👍"; up.title=I18N.t("sre.helpful","有用");
  const down=document.createElement("button"); down.type="button"; down.textContent="👎"; down.title=I18N.t("sre.unhelpful","无用");
  const sendFb=async (action)=>{
    const runId=opts.runId||div.dataset.runId||"";
    if(!runId){
      if(typeof toast==="function") toast(I18N.t("sre.feedback_need_run","本次回复缺少 run_id，无法闭环反馈"),"err");
      return;
    }
    let reason="";
    if(action==="unhelpful"){
      reason=await requestAIFeedbackReason({
        title:I18N.t("sre.improve_ai","帮助 AI 改进"),
        message:I18N.t("sre.unhelpful_reason","请简要说明为何无用（将写入避坑记忆）：")
      });
      if(reason===null) return;
      reason=(reason||"").trim();
      if(!reason){ if(typeof toast==="function") toast(I18N.t("sre.need_unhelpful_reason","差评需填写原因"),"err"); return; }
    }
    let feedbackResult=null;
    try{
      const r=await fetch(`${API}/ai/assist/feedback`,{method:"POST",headers:{"Content-Type":"application/json"},
        body:JSON.stringify({assist_id:runId,run_id:runId,action,reason})});
      const j=await r.json().catch(()=>({}));
      if(!r.ok){ if(typeof toast==="function") toast(j.error||I18N.t("sre.feedback_failed","反馈失败"),"err"); return; }
      feedbackResult=j;
    }catch(e){
      if(typeof toast==="function") toast(I18N.t("sre.feedback_not_saved","反馈未保存，请检查网络后重试"),"err");
      return;
    }
    up.style.display="none"; down.style.display="none";
    if(typeof toast==="function"){
      const learned=!!(feedbackResult&&feedbackResult.learning_queued);
      toast(learned
        ? (action==="helpful"?I18N.t("sre.marked_helpful","已标记为有用 👍"):I18N.t("sre.marked_unhelpful","已标记为无用 👎"))
        : I18N.t("assist.fb_recorded_no_memory","反馈已记录；持久记忆不可用，本次未进入跨会话学习"),learned?"ok":"warn");
    }
  };
  up.onclick=()=>sendFb("helpful");
  down.onclick=()=>sendFb("unhelpful");
  bar.appendChild(up); bar.appendChild(down);
  div.appendChild(bar);
}
// 重答：取最近一条用户提问重新发送（追加一轮新回答）
function regenerateAIChat(){
  if(_aiChatBusy){ if(typeof toast==="function") toast(I18N.t("sre.gen_stop_first_regen","生成中，请先终止再重答"),"err"); return; }
  let q=""; for(let i=AI_CHAT_HISTORY.length-1;i>=0;i--){ if(AI_CHAT_HISTORY[i].role==="user"){ q=AI_CHAT_HISTORY[i].content; break; } }
  if(!q){ if(typeof toast==="function") toast(I18N.t("sre.no_regen","暂无可重答的问题"),"err"); return; }
  const inp=$("aiChatInput"); if(inp){ inp.value=q; if(typeof autoGrowAIInput==="function") autoGrowAIInput(); }
  sendAIChat();
}
// 附件预览渲染（图片缩略图 / 文档 chip，可预览与删除）
function renderAttachments(){
  const box=$("aiChatAttach"); if(!box) return;
  if(!AI_ATTACHMENTS.length){ box.innerHTML=""; box.style.display="none"; return; }
  box.style.display="flex";
  box.innerHTML=AI_ATTACHMENTS.map((a,i)=>{
    if(a.kind==="image"&&a.data){
      const src=`data:${a.mime||"image/png"};base64,${a.data}`;
      return `<span class="ai-attach-chip ai-attach-image" title="${esc(a.name)}"><img src="${src}" alt="${esc(a.name)}" data-att-preview="${i}"><span class="ai-attach-name" data-att-preview="${i}">${esc(a.name)}</span><button data-att="${i}" title="${I18N.t("sre.remove","移除")}">✕</button></span>`;
    }
    const icon=/\.pdf$/i.test(a.name)?"📕":(/\.(docx?|xlsx?)$/i.test(a.name)?"📘":"📄");
    const status=a.text===I18N.t("sre.parsing","（解析中…）")||a.text===I18N.t("sre.fetching_web","（抓取中…）")?" parsing":"";
    return `<span class="ai-attach-chip${status}" title="${esc(a.name)}"><button type="button" class="ai-attach-open" data-att-preview="${i}">${icon} ${esc(a.name)}</button><button data-att="${i}" title="${I18N.t("sre.remove","移除")}">✕</button></span>`;
  }).join("");
  box.querySelectorAll("[data-att]").forEach(b=>b.onclick=(ev)=>{ ev.stopPropagation(); AI_ATTACHMENTS.splice(parseInt(b.dataset.att),1); renderAttachments(); });
  box.querySelectorAll("[data-att-preview]").forEach(el=>{
    el.onclick=(ev)=>{
      ev.preventDefault();
      const idx=parseInt(el.getAttribute("data-att-preview"),10);
      previewAIAttachment(AI_ATTACHMENTS[idx]);
    };
  });
}
function previewAIAttachment(a){
  if(!a) return;
  const mask=$("aiAttachPreviewMask"), title=$("aiAttachPreviewTitle"), body=$("aiAttachPreviewBody");
  if(!mask||!body){
    // 兜底：无预览弹层时用新窗口
    if(a.kind==="image"&&a.data){
      const w=window.open(); if(w) w.document.write(`<img src="data:${a.mime||"image/png"};base64,${a.data}" style="max-width:100%">`);
    } else if(a.data && /\.pdf$/i.test(a.name||"")){
      const bin=atob(a.data); const u8=new Uint8Array(bin.length); for(let i=0;i<bin.length;i++) u8[i]=bin.charCodeAt(i);
      const url=URL.createObjectURL(new Blob([u8],{type:"application/pdf"}));
      window.open(url,"_blank");
    } else if(a.text){
      const w=window.open(); if(w){ w.document.title=a.name||"preview"; w.document.body.innerHTML=`<pre style="white-space:pre-wrap;font:13px/1.5 ui-monospace,monospace;padding:16px">${esc(a.text)}</pre>`; }
    }
    return;
  }
  if(title) title.textContent=a.name||I18N.t("sre.attachment_preview","附件预览");
  if(a.kind==="image"&&a.data){
    body.innerHTML=`<img class="ai-attach-lightbox" src="data:${a.mime||"image/png"};base64,${a.data}" alt="${esc(a.name||"")}">`;
  } else if(a.data && /\.pdf$/i.test(a.name||"")){
    try{
      const bin=atob(a.data); const u8=new Uint8Array(bin.length); for(let i=0;i<bin.length;i++) u8[i]=bin.charCodeAt(i);
      const url=URL.createObjectURL(new Blob([u8],{type:"application/pdf"}));
      body.innerHTML=`<iframe class="ai-attach-pdf" src="${url}" title="${esc(a.name||"pdf")}"></iframe><div class="hint" style="margin-top:8px">下方为解析文本预览：</div><pre class="ai-attach-text">${esc((a.text||"").slice(0,12000))}</pre>`;
    }catch(e){
      body.innerHTML=`<pre class="ai-attach-text">${esc(a.text||String(e))}</pre>`;
    }
  } else {
    const note=a.text?"":`<div class="hint">${I18N.t("sre.no_preview_text","暂无可预览文本（文件可能仍在解析）")}</div>`;
    body.innerHTML=note+`<pre class="ai-attach-text">${esc(a.text||"")}</pre>`;
  }
  mask.classList.add("show");
}
// 需服务端解析的二进制文档（其余文本文件前端直接读文本）
const _AI_PARSE_EXT=["docx","xlsx","pdf"];
function _extOf(name){ const i=String(name||"").lastIndexOf("."); return i>=0?name.slice(i+1).toLowerCase():""; }
// 选择图片/文件：图片读为 base64（视觉）；docx/xlsx/pdf 经服务端解析成文本；纯文本文件直接读文本。
function onAIChatFiles(ev){
  const files=Array.from((ev.target&&ev.target.files)||[]);
  for(const f of files){
    if(f.type&&f.type.startsWith("image/")){
      if(AI_ATTACHMENTS.filter(a=>a.kind==="image").length>=4){ if(typeof toast==="function") toast(I18N.t("sre.max_4_images","最多 4 张图片"),"err"); continue; }
      if(f.size>4*1024*1024){ if(typeof toast==="function") toast(`${I18N.t("sre.image","图片")} ${f.name} ${I18N.t("sre.exceeds_4mb","超过 4MB")}`,"err"); continue; }
      const rd=new FileReader();
      rd.onload=()=>{ const s=String(rd.result||""); const c=s.indexOf(","); AI_ATTACHMENTS.push({kind:"image",name:f.name,mime:f.type||"image/png",data:c>=0?s.slice(c+1):s}); renderAttachments(); };
      rd.readAsDataURL(f);
    } else if(_AI_PARSE_EXT.includes(_extOf(f.name))){
      if(f.size>10*1024*1024){ if(typeof toast==="function") toast(`${I18N.t("sre.file","文件")} ${f.name} ${I18N.t("sre.exceeds_10mb","超过 10MB")}`,"err"); continue; }
      parseFileAttachment(f); // 二进制文档 → 服务端解析
    } else {
      if(f.size>1024*1024){ if(typeof toast==="function") toast(`${I18N.t("sre.file","文件")} ${f.name} ${I18N.t("sre.exceeds_1mb_hint","超过 1MB，请上传关键片段")}`,"err"); continue; }
      const rd=new FileReader();
      rd.onload=()=>{ AI_ATTACHMENTS.push({kind:"file",name:f.name,text:String(rd.result||"")}); renderAttachments(); };
      rd.readAsText(f);
    }
  }
  if(ev.target) ev.target.value=""; // 允许重复选同一文件
}
// docx/xlsx/pdf → base64 → POST /hermes/parse → 提取文本作为附件
function parseFileAttachment(f){
  const rd=new FileReader();
  rd.onload=async()=>{
    const s=String(rd.result||""); const c=s.indexOf(","); const b64=c>=0?s.slice(c+1):s;
    const ph={kind:"file",name:f.name,text:I18N.t("sre.parsing","（解析中…）"),mime:f.type||"",data:b64};
    AI_ATTACHMENTS.push(ph); renderAttachments();
    try{
      const r=await fetch(`${API}/hermes/parse`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({name:f.name,mime:f.type||"",data:b64})});
      const j=await r.json().catch(()=>({}));
      if(!r.ok||j.error){ AI_ATTACHMENTS=AI_ATTACHMENTS.filter(a=>a!==ph); if(typeof toast==="function") toast(`${I18N.t("sre.parse_v","解析")} ${f.name} ${I18N.t("sre.failed_v","失败")}：${(j&&j.error)||r.status}`,"err"); renderAttachments(); return; }
      ph.text=j.text||""; ph.data=b64; ph.mime=f.type||ph.mime; renderAttachments();
      if(typeof toast==="function") toast(`${I18N.t("sre.parsed_v","已解析")} ${f.name}（${j.chars||0} ${I18N.t("sre.chars_unit","字")}${j.truncated?I18N.t("sre.truncated","，已截断"):""}）`,"ok");
    }catch(e){ AI_ATTACHMENTS=AI_ATTACHMENTS.filter(a=>a!==ph); if(typeof toast==="function") toast(`${I18N.t("sre.parse_v","解析")} ${f.name} ${I18N.t("sre.failed_v","失败")}`,"err"); renderAttachments(); }
  };
  rd.readAsDataURL(f);
}
// 识别 URL：抓取网页正文作为附件注入上下文
async function attachURL(){
  const u=await requestAITextInput({
    title:I18N.t("sre.import_web_title","导入网页知识"),
    message:I18N.t("sre.url_prompt","输入要抓取的网页 URL（将提取正文注入对话）："),
    label:"URL",placeholder:"https://example.com/runbook",
    submitLabel:I18N.t("sre.fetch_web","读取网页"),danger:false,rows:2,maxLength:2048,
    requiredMessage:I18N.t("sre.url_required","请输入有效的 HTTP(S) URL")
  });
  if(!u||!u.trim()) return;
  let parsedURL;
  try{
    parsedURL=new URL(u.trim());
    if(!["http:","https:"].includes(parsedURL.protocol)) throw new Error("unsupported protocol");
  }catch(e){
    if(typeof toast==="function") toast(I18N.t("sre.url_required","请输入有效的 HTTP(S) URL"),"err");
    return;
  }
  const normalizedURL=parsedURL.toString();
  const ph={kind:"file",name:normalizedURL.slice(0,60),text:I18N.t("sre.fetching_web","（抓取中…）")};
  AI_ATTACHMENTS.push(ph); renderAttachments();
  try{
    const r=await fetch(`${API}/hermes/parse`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({url:normalizedURL})});
    const j=await r.json().catch(()=>({}));
    if(!r.ok||j.error){ AI_ATTACHMENTS=AI_ATTACHMENTS.filter(a=>a!==ph); if(typeof toast==="function") toast(`${I18N.t("sre.fetch_failed","抓取失败")}：${(j&&j.error)||r.status}`,"err"); renderAttachments(); return; }
    ph.text=`[来源 URL: ${normalizedURL}]\n`+(j.text||""); renderAttachments();
    if(typeof toast==="function") toast(`${I18N.t("sre.fetched","已抓取")}（${j.chars||0} ${I18N.t("sre.chars_unit","字")}${j.truncated?I18N.t("sre.truncated","，已截断"):""}）`,"ok");
  }catch(e){ AI_ATTACHMENTS=AI_ATTACHMENTS.filter(a=>a!==ph); if(typeof toast==="function") toast(I18N.t("sre.fetch_failed","抓取失败"),"err"); renderAttachments(); }
}
safeAddEventListener("logSearchBtn","click",searchLogs);
safeAddEventListener("logKeyword","keydown",e=>{ if(e.key==="Enter") searchLogs(); });
safeAddEventListener("logSource","change",()=>{ onLogSourceChange(); if(!$("logSource").value) searchLogs(); });
safeAddEventListener("logJob","change",()=>{ onLogJobChange(); });
safeAddEventListener("aiInspectBtn","click",runInspect);
safeAddEventListener("dutyReportBtn","click",genDutyReport);
safeAddEventListener("copilotBtn","click",openCopilot);
safeAddEventListener("copilotRefreshBtn","click",loadCopilot);
safeAddEventListener("copilotBriefBtn","click",genCopilotBrief);
safeAddEventListener("skillsPackImportBtn","click",importSkillPacksNow);
safeAddEventListener("skillsBtn","click",openSkills);
safeAddEventListener("skillsDistillBtn","click",distillSkillsNow);
safeAddEventListener("skillsShowArchived","change",loadSkills);
safeAddEventListener("memoryBtn","click",openMemories);
safeAddEventListener("memoryKindFilter","change",loadMemories);
safeAddEventListener("memoryVerifiedFilter","change",loadMemories);
async function loadABExperiments(){
  const el=$("abExpList"); if(!el) return;
  try{
    const j=await fetch(`${API}/ai/experiments`).then(r=>r.json());
    const list=j.experiments||[];
    if(!list.length){ el.textContent="暂无实验定义"; return; }
    el.innerHTML=list.map(e=>{
      const vars=Object.entries(e.variants||{}).map(([k,v])=>`${k}:${v}%`).join(" ");
      return `<div class="sre-row" style="padding:6px 0"><div class="sre-row-main"><div class="mono">${esc(e.id)} · ${esc(e.name||"")}${e.enabled?"":" · 停用"}</div>
        <div class="sre-row-sub">${esc(e.task||"全部任务")} · ${esc(vars)}</div></div>
        <button class="btn danger sm" type="button" data-ab-del="${esc(e.id)}">删除</button></div>`;
    }).join("");
    el.querySelectorAll("[data-ab-del]").forEach(btn=>{
      btn.onclick=async()=>{
        if(!confirm("删除实验 "+btn.dataset.abDel+"？")) return;
        const r=await fetch(`${API}/ai/experiments/${encodeURIComponent(btn.dataset.abDel)}`,{method:"DELETE"});
        if(r.ok){ toast("已删除","ok"); loadABExperiments(); } else toast("删除失败","err");
      };
    });
  }catch(e){ el.textContent="加载失败"; }
}
async function saveABExperiment(){
  let variants={}, models={};
  try{ variants=JSON.parse(($("abExpVariants")&&$("abExpVariants").value)||'{"control":50,"treatment":50}'); }catch(e){ toast("变体 JSON 无效","err"); return; }
  try{ const raw=($("abExpModels")&&$("abExpModels").value||"").trim(); if(raw) models=JSON.parse(raw); }catch(e){ toast("模型映射 JSON 无效","err"); return; }
  const id=($("abExpId")&&$("abExpId").value||"").trim();
  if(!id){ toast("请填写实验 ID","err"); return; }
  const body={ id, name:($("abExpName")&&$("abExpName").value||id).trim(), task:($("abExpTask")&&$("abExpTask").value||"").trim(),
    enabled:!!($("abExpEnabled")&&$("abExpEnabled").checked), variants, variant_models:models };
  const r=await fetch(`${API}/ai/experiments`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
  const j=await r.json().catch(()=>({}));
  if(r.ok){ toast("实验已保存","ok"); loadABExperiments(); } else toast(j.error||"保存失败","err");
}
safeAddEventListener("abExpSaveBtn","click",saveABExperiment);
safeAddEventListener("abExpRefreshBtn","click",loadABExperiments);
safeAddEventListener("aiStatsRefreshBtn","click",()=>{ loadAIStats(); loadABExperiments(); });
safeAddEventListener("aiStatsRange","change",()=>{
  const days=parseInt(($("aiStatsRange")&&$("aiStatsRange").value)||"7",10)||7;
  if(typeof clearAnchoredRange==="function") clearAnchoredRange("ai-usage:"+days);
  // Also clear other day anchors so switching ranges always re-freezes to now.
  [1,3,7,14,30].forEach(d=>{ if(d!==days&&typeof clearAnchoredRange==="function") clearAnchoredRange("ai-usage:"+d); });
  loadAIStats();
});
safeAddEventListener("aiConfigBtn","click",openAIConfig);
safeAddEventListener("aiChatSettingsBtn","click",openAIConfig);
safeAddEventListener("settingsAiConfigBtn","click",()=>{
  const sm=$("settingsMask"); if(sm) sm.classList.remove("show");
  if(typeof openAIConfig==="function") openAIConfig();
});
safeAddEventListener("aiConfigSaveBtn","click",()=>saveAIConfig());
safeAddEventListener("aiConfigSaveCloseBtn","click",()=>saveAIConfigAndClose());
document.querySelectorAll(".ai-nav-item").forEach(btn=>{
  btn.addEventListener("click",()=>switchAISettingsTab(btn.getAttribute("data-ai-tab")));
});
safeAddEventListener("aiChatTestBtn","click",testAIChatConfig);
safeAddEventListener("aiSpeechTestBtn","click",testAISpeechConfig);
safeAddEventListener("aiEmbedTestBtn","click",testAIEmbedConfig);
safeAddEventListener("aiRerankTestBtn","click",testAIRerankConfig);
safeAddEventListener("aiWeKnoraTestBtn","click",testAIWeKnoraConfig);
safeAddEventListener("aiWeKnoraListBtn","click",listAIWeKnoraKBs);
safeAddEventListener("aiJumpObserveAbBtn","click",()=>{ switchAISettingsTab("observe"); setAiCardCollapsed("qualityRoute", false); });
safeAddEventListener("aiJumpQualityRouteBtn","click",()=>{ switchAISettingsTab("quality"); setAiCardCollapsed("qualityRoute", false); });
function bindAiCardHeader(key){
  const def=AI_CARD_DEFS[key]; if(!def) return;
  const el=$(def.header); if(!el || el._aiCardBound) return;
  el._aiCardBound=true;
  el.addEventListener("click", ev=>toggleAiCard(key, ev));
  el.addEventListener("keydown", e=>{
    if(e.key==="Enter" || e.key===" "){ e.preventDefault(); toggleAiCard(key, e); }
  });
}
Object.keys(AI_CARD_DEFS).forEach(bindAiCardHeader);
applyAiCardCollapsedState();
const _aiMask=$("aiConfigMask");
if(_aiMask && !_aiMask._aiCloseGuard){
  _aiMask._aiCloseGuard=true;
  _aiMask.addEventListener("click", e=>{
    if(e.target===_aiMask || (e.target && e.target.closest && e.target.closest("[data-close-btn]"))){
      e.stopPropagation();
      e.preventDefault();
      tryCloseAIConfigMask();
    }
  }, true);
}
safeAddEventListener("mcpEnabled","change",()=>{ updateMcpCardSummary(); markAIConfigDirty(); });
safeAddEventListener("mcpToken","input",refreshMcpClientConfig);
safeAddEventListener("mcpCopyClientCfgBtn","click",copyMcpClientConfig);
safeAddEventListener("mcpRefreshClientCfgBtn","click",refreshMcpClientConfig);
safeAddEventListener("mcpClientAddBtn","click",()=>openMCPClientEditor(null));
safeAddEventListener("mcpClientCancelBtn","click",()=>{ const ed=$("mcpClientEditor"); if(ed) ed.hidden=true; });
safeAddEventListener("mcpClientSaveBtn","click",saveMCPClientEditorToList);
safeAddEventListener("mcpClientTestBtn","click",testMCPClientEditor);
safeAddEventListener("mcpClientSyncBtn","click",syncMCPClientEditor);
document.addEventListener("click",(e)=>{
  const edit=e.target && e.target.closest && e.target.closest("[data-mcp-edit]");
  if(edit){ openMCPClientEditor(parseInt(edit.getAttribute("data-mcp-edit"),10)); return; }
  const del=e.target && e.target.closest && e.target.closest("[data-mcp-del]");
  if(del){
    const i=parseInt(del.getAttribute("data-mcp-del"),10);
    if(!Number.isFinite(i)) return;
    MCP_CLIENTS.splice(i,1);
    syncMCPClientsHidden();
    renderMCPClientsList();
    updateMcpCardSummary();
  }
});
safeAddEventListener("weknoraEnabled","change",updateWeKnoraCardSummary);
safeAddEventListener("weknoraURL","change",updateWeKnoraCardSummary);
safeAddEventListener("mcpGenTokenBtn","click",()=>{
  const t=$("mcpToken"); if(!t) return;
  t.type="text"; // 明文显示便于复制保存
  t.value=genStrongToken(32);
  if($("mcpEnabled")) $("mcpEnabled").checked=true; // 生成即视为要启用
  updateMcpCardSummary();
  if(typeof toast==="function") toast(I18N.t("sre.token_generated","已生成高强度随机令牌，请及时保存"),"ok");
});
safeAddEventListener("aiModelRefreshBtn","click",loadAIModels);
safeAddEventListener("aiEndpoint","change",()=>{ syncAIPresetActive(); loadAIModels(); updateProviderPanelSummary(); });
safeAddEventListener("aiEnabled","change",()=>{ updateProviderPanelSummary(); updateAISettingsNavDots(); markAIConfigDirty(); });
safeAddEventListener("aiModel","change",()=>{ updateProviderPanelSummary(); updateAISettingsNavDots(); });
safeAddEventListener("aiKey","change",loadAIModels); // 填/改 API Key 后自动获取模型
safeAddEventListener("aiModelCaretBtn","click",toggleModelDropdown);
safeAddEventListener("aiModel","focus",showModelDropdown);
safeAddEventListener("aiModel","input",e=>{ renderModelDropdown(e.target.value); const dd=$("aiModelDropdown"); if(dd) dd.style.display="block"; });
document.addEventListener("click",e=>{ if(!e.target.closest || !e.target.closest(".ai-model-wrap")) hideModelDropdown(); });
safeAddEventListener("aiTermToggleBtn","click",toggleAITerm);
safeAddEventListener("aiTermConfirmBtn","click",confirmAITerm);
safeAddEventListener("aiTermCancelBtn","click",()=>{ const r=$("aiTermPwRow"); if(r) r.style.display="none"; const m=$("aiTermMsg"); if(m){ m.textContent=""; m.className="ai-term-msg"; } });
safeAddEventListener("aiTermPw","keydown",e=>{ if(e.key==="Enter"){ e.preventDefault(); confirmAITerm(); } });
safeAddEventListener("aiChatBtn","click",openAIChat);
safeAddEventListener("topAiBtn","click",openAIChat); // 顶栏 AI 对话入口（全局可达）
safeAddEventListener("aiChatSendBtn","click",sendAIChat);
safeAddEventListener("aiChatInput","keydown",e=>{ if(e.key==="Enter"&&!e.shiftKey){ e.preventDefault(); sendAIChat(); } });
safeAddEventListener("aiChatInput","input",autoGrowAIInput);
safeAddEventListener("aiChatLog","scroll",()=>{ const b=$("aiChatScrollBtn"); if(b) b.style.display=aiChatStick()?"none":"flex"; });
safeAddEventListener("aiChatScrollBtn","click",()=>{ aiChatToBottom(); const b=$("aiChatScrollBtn"); if(b) b.style.display="none"; });
safeAddEventListener("aiChatAttachBtn","click",()=>{ const f=$("aiChatFile"); if(f) f.click(); });
safeAddEventListener("aiChatUrlBtn","click",()=>{ closeAIComposerMenu(); attachURL(); });
safeAddEventListener("aiChatFile","change",onAIChatFiles);
safeAddEventListener("aiChatMicBtn","click",toggleAIVoiceInput);
safeAddEventListener("aiChatStopBtn","click",stopAIChat);
function closeAIComposerMenu(){
  const menu=$("aiComposerMenu"), btn=$("aiComposerMoreBtn");
  if(menu) menu.hidden=true;
  if(btn) btn.setAttribute("aria-expanded","false");
}
function toggleAIComposerMenu(ev){
  if(ev) ev.stopPropagation();
  const menu=$("aiComposerMenu"), btn=$("aiComposerMoreBtn");
  if(!menu||!btn) return;
  const open=menu.hidden;
  menu.hidden=!open;
  btn.setAttribute("aria-expanded", open?"true":"false");
}
safeAddEventListener("aiComposerMoreBtn","click",toggleAIComposerMenu);
document.addEventListener("click",e=>{
  const wrap=document.querySelector(".ai-composer-more");
  if(wrap&&!wrap.contains(e.target)) closeAIComposerMenu();
});
safeAddEventListener("aiWriteApprovalBtn","click",async()=>{
  closeAIComposerMenu();
  const tool=prompt("签发写工具审批：工具名（如 k8s_scale）","k8s_scale");
  if(!tool) return;
  try{
    const r=await fetch(`${API}/ai/write-approval`,{method:"POST",headers:{"Content-Type":"application/json"},
      body:JSON.stringify({tool:tool.trim(),ttl_sec:600})});
    const j=await r.json().catch(()=>({}));
    if(!r.ok){ toast(j.error||"签发失败","err"); return; }
    window._AI_WRITE_APPROVAL={id:j.approval_id,tool:j.tool,expires_at:j.expires_at,args_hash:j.args_hash||""};
    const hint=$("aiWriteApprovalHint");
    if(hint){
      hint.style.display="";
      hint.innerHTML=`<span class="badge ok">写审批</span> <span class="mono">${esc(j.tool)}</span> · <code>${esc(j.approval_id)}</code> · ${j.ttl_sec||600}s 内下一则消息自动附带`;
    }
    toast("写审批已签发","ok");
  }catch(e){ toast(String(e),"err"); }
});
safeAddEventListener("skillsExportBtn","click",exportCustomerSkills);
safeAddEventListener("skillsImportBtn","click",()=>{ const f=$("skillsImportFile"); if(f) f.click(); });
safeAddEventListener("skillsImportFile","change",e=>{
  const f=e.target&&e.target.files&&e.target.files[0];
  if(f) importCustomerSkillsFile(f);
  if(e.target) e.target.value="";
});
safeAddEventListener("aiUndoBtn","click",undoAIChat);
safeAddEventListener("aiNewChatBtn","click",newAIChat);
safeAddEventListener("aiSessionSelect","change",e=>switchAISession(e.target.value));

/* ---- Web Speech：语音输入 / 朗读回复（系统语音 + 可选云端模型） ---- */
let _aiVoiceRec=null, _aiVoiceOn=false, _aiMediaRec=null, _aiMediaChunks=[], _aiMediaStream=null;
let _aiSpeakBtn=null, _aiCloudAudio=null;
let AI_SPEECH_STATUS={prefer:false,stt:false,tts:false};
async function refreshAISpeechStatus(){
  try{
    const r=await fetch(`${API}/ai/speech/status`);
    if(!r.ok) return;
    const j=await r.json();
    AI_SPEECH_STATUS={prefer:!!j.prefer_cloud,stt:!!j.stt_ready,tts:!!j.tts_ready};
  }catch(e){}
}
function useCloudSTT(){ return !!(AI_SPEECH_STATUS.prefer && AI_SPEECH_STATUS.stt); }
function useCloudTTS(){ return !!(AI_SPEECH_STATUS.prefer && AI_SPEECH_STATUS.tts); }

async function toggleAIVoiceInput(){
  const btn=$("aiChatMicBtn");
  if(_aiVoiceOn){ stopAIVoiceInput(); return; }
  await refreshAISpeechStatus();
  if(useCloudSTT()){
    try{ await startCloudVoiceInput(btn); return; }
    catch(e){ if(typeof toast==="function") toast(I18N.t("sre.cloud_stt_fallback","云端语音不可用，尝试浏览器识别…"),"warn"); }
  }
  const SR=window.SpeechRecognition||window.webkitSpeechRecognition;
  if(!SR){ toast(I18N.t("sre.voice_unsupported","当前浏览器不支持语音输入（建议 Chrome / Edge，或在 AI 设置配置 STT 模型）"),"err"); return; }
  try{
    _aiVoiceRec=new SR();
    _aiVoiceRec.lang="zh-CN";
    _aiVoiceRec.interimResults=true;
    _aiVoiceRec.continuous=false;
    _aiVoiceRec.onresult=e=>{
      let final="", interim="";
      for(let i=e.resultIndex;i<e.results.length;i++){
        const t=e.results[i][0].transcript;
        if(e.results[i].isFinal) final+=t; else interim+=t;
      }
      const inp=$("aiChatInput"); if(!inp) return;
      if(final){ inp.value=(inp.value?inp.value+" ":"")+final.trim(); autoGrowAIInput({target:inp}); }
    };
    _aiVoiceRec.onerror=()=>{ _aiVoiceOn=false; if(btn) btn.classList.remove("active"); };
    _aiVoiceRec.onend=()=>{ _aiVoiceOn=false; if(btn) btn.classList.remove("active"); };
    _aiVoiceRec.start(); _aiVoiceOn=true; if(btn) btn.classList.add("active");
  }catch(e){ toast(I18N.t("sre.voice_start_failed","无法启动语音输入"),"err"); }
}
function stopAIVoiceInput(){
  const btn=$("aiChatMicBtn");
  if(_aiVoiceRec){ try{_aiVoiceRec.stop();}catch(e){} _aiVoiceRec=null; }
  if(_aiMediaRec && _aiMediaRec.state!=="inactive"){
    try{ _aiMediaRec.stop(); }catch(e){}
  } else {
    _aiVoiceOn=false; if(btn) btn.classList.remove("active");
    if(_aiMediaStream){ try{_aiMediaStream.getTracks().forEach(t=>t.stop());}catch(e){} _aiMediaStream=null; }
  }
}
async function startCloudVoiceInput(btn){
  if(!navigator.mediaDevices||!navigator.mediaDevices.getUserMedia) throw new Error("no media");
  _aiMediaChunks=[];
  _aiMediaStream=await navigator.mediaDevices.getUserMedia({audio:true});
  const mime=MediaRecorder.isTypeSupported("audio/webm")?"audio/webm":"";
  _aiMediaRec=mime?new MediaRecorder(_aiMediaStream,{mimeType:mime}):new MediaRecorder(_aiMediaStream);
  _aiMediaRec.ondataavailable=e=>{ if(e.data&&e.data.size) _aiMediaChunks.push(e.data); };
  _aiMediaRec.onstop=async()=>{
    _aiVoiceOn=false; if(btn) btn.classList.remove("active");
    if(_aiMediaStream){ try{_aiMediaStream.getTracks().forEach(t=>t.stop());}catch(e){} _aiMediaStream=null; }
    const blob=new Blob(_aiMediaChunks,{type:_aiMediaRec.mimeType||"audio/webm"});
    _aiMediaRec=null; _aiMediaChunks=[];
    if(!blob.size){ if(typeof toast==="function") toast(I18N.t("sre.voice_empty","未采集到有效音频"),"err"); return; }
    if(typeof toast==="function") toast(I18N.t("sre.voice_recognizing","正在识别…"),"ok");
    try{
      const fd=new FormData();
      fd.append("file", blob, "speech.webm");
      const r=await fetch(`${API}/ai/speech/stt`,{method:"POST",body:fd});
      const j=await r.json().catch(()=>({}));
      if(!r.ok||!j.text){ throw new Error(j.error||("HTTP "+r.status)); }
      const inp=$("aiChatInput"); if(inp){ inp.value=(inp.value?inp.value+" ":"")+String(j.text).trim(); autoGrowAIInput({target:inp}); }
    }catch(e){ if(typeof toast==="function") toast(I18N.t("sre.voice_stt_failed","语音识别失败")+"："+e,"err"); }
  };
  _aiMediaRec.start();
  _aiVoiceOn=true; if(btn) btn.classList.add("active");
  if(typeof toast==="function") toast(I18N.t("sre.voice_cloud_listening","云端聆听中，再次点击结束"),"ok");
}

// 挑选「成熟稳重」的中文女声，明确排除男声；语速/音调在 speakAIText 中配合下调。
function pickPreferredAIVoice(){
  if(!window.speechSynthesis) return null;
  const voices=speechSynthesis.getVoices()||[];
  if(!voices.length) return null;
  const label=v=>((v.name||"")+" "+(v.voiceURI||"")+" "+(v.lang||"")).toLowerCase();
  const isMale=v=>/yunyang|yunxi|yunjian|yunhao|yunfeng|kangkang|male|\bman\b|男声|男/.test(label(v));
  const isZh=v=>/zh|chinese|中文|普通话|国语|cmn/.test(label(v));
  const zh=voices.filter(isZh);
  const pool=(zh.length?zh:voices).filter(v=>!isMale(v));
  const fallback=zh.length?zh:voices;
  // 成熟稳重优先：晓萱/晓涵/慧慧/晓伊；避免偏幼的瑶瑶等排在最前
  const mature=/xiaoxuan|xiaohan|huihui|xiaoyi|xiaoqiu|xiaorou|xiaoyan|xiaomeng|xiaoshuang/;
  const female=/xiaoxiao|xiaochen|female|女声|女|\bwoman\b/;
  const quality=/neural|natural|premium|enhanced|online/;
  let best=pool.find(v=>mature.test(label(v)))
    ||pool.find(v=>female.test(label(v))&&quality.test(label(v)))
    ||pool.find(v=>female.test(label(v)))
    ||pool.find(v=>quality.test(label(v)))
    ||pool.find(v=>/zh-CN|zh_CN|cmn-Hans/i.test(v.lang||""))
    ||pool[0]
    ||fallback.find(v=>!isMale(v))
    ||fallback[0]
    ||null;
  return best;
}
function normalizeSpeakText(raw){
  return String(raw||"")
    .replace(/```[\s\S]*?```/g," ")
    .replace(/`[^`]+`/g," ")
    .replace(/!\[[^\]]*\]\([^)]+\)/g," ")
    .replace(/\[([^\]]+)\]\([^)]+\)/g,"$1")
    .replace(/[#>*_~|]/g," ")
    .replace(/\s+/g," ")
    .trim();
}
async function speakAITextCloud(text, btn){
  if(_aiCloudAudio){ try{_aiCloudAudio.pause();}catch(e){} _aiCloudAudio=null; }
  if(btn){ btn.classList.add("speaking"); _aiSpeakBtn=btn; }
  const r=await fetch(`${API}/ai/speech/tts`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({text})});
  if(!r.ok){
    const j=await r.json().catch(()=>({}));
    throw new Error(j.error||("HTTP "+r.status));
  }
  const blob=await r.blob();
  const url=URL.createObjectURL(blob);
  const audio=new Audio(url);
  _aiCloudAudio=audio;
  const cleanup=()=>{
    if(_aiSpeakBtn){ _aiSpeakBtn.classList.remove("speaking"); _aiSpeakBtn=null; }
    try{URL.revokeObjectURL(url);}catch(e){}
    if(_aiCloudAudio===audio) _aiCloudAudio=null;
  };
  audio.onended=audio.onerror=cleanup;
  await audio.play();
}
async function speakAIText(rawText, btn){
  const text=normalizeSpeakText(rawText).slice(0,1600);
  if(!text){ toast(I18N.t("sre.no_ai_reply","暂无可朗读的 AI 回复"),"err"); return; }
  if(btn && btn.classList.contains("speaking")){
    try{ speechSynthesis.cancel(); }catch(e){}
    if(_aiCloudAudio){ try{_aiCloudAudio.pause();}catch(e){} _aiCloudAudio=null; }
    btn.classList.remove("speaking");
    _aiSpeakBtn=null;
    return;
  }
  document.querySelectorAll(".ai-speak-btn.speaking").forEach(b=>{
    b.classList.remove("speaking");
  });
  try{ speechSynthesis.cancel(); }catch(e){}
  if(_aiCloudAudio){ try{_aiCloudAudio.pause();}catch(e){} _aiCloudAudio=null; }

  await refreshAISpeechStatus();
  if(useCloudTTS()){
    try{ await speakAITextCloud(text, btn); return; }
    catch(e){ if(typeof toast==="function") toast(I18N.t("sre.cloud_tts_fallback","云端播报失败，改用浏览器朗读"),"warn"); }
  }
  if(!window.speechSynthesis){ toast(I18N.t("sre.tts_unsupported","当前浏览器不支持语音朗读"),"err"); return; }
  const u=new SpeechSynthesisUtterance(text);
  u.lang="zh-CN";
  u.rate=0.88;
  u.pitch=0.96;
  u.volume=1;
  const voice=pickPreferredAIVoice();
  if(voice){ u.voice=voice; if(voice.lang) u.lang=voice.lang; }
  if(btn){
    btn.classList.add("speaking");
    _aiSpeakBtn=btn;
  }
  u.onend=u.onerror=()=>{
    if(_aiSpeakBtn){ _aiSpeakBtn.classList.remove("speaking"); _aiSpeakBtn=null; }
  };
  const speakNow=()=>speechSynthesis.speak(u);
  if(!voice && speechSynthesis.getVoices().length===0){
    speechSynthesis.onvoiceschanged=()=>{
      const v=pickPreferredAIVoice();
      if(v){ u.voice=v; if(v.lang) u.lang=v.lang; }
      speechSynthesis.onvoiceschanged=null;
      speakNow();
    };
    setTimeout(speakNow, 250);
  } else speakNow();
}
function speakLastAIReply(){
  const log=$("aiChatLog"); if(!log) return;
  const bubbles=[...log.querySelectorAll(".ai-chat-msg.ai")];
  let text="";
  let btn=null;
  if(bubbles.length){
    const last=bubbles[bubbles.length-1];
    btn=last.querySelector(".ai-speak-btn");
    text=(last.innerText||last.textContent||"").trim();
  } else if(typeof AI_CHAT_HISTORY!=="undefined"){
    for(let i=AI_CHAT_HISTORY.length-1;i>=0;i--){ if(AI_CHAT_HISTORY[i].role==="assistant"&&AI_CHAT_HISTORY[i].content){ text=AI_CHAT_HISTORY[i].content; break; } }
  }
  speakAIText(text, btn);
}
// 预热 voices 列表（Chrome 首次 getVoices 常为空）
if(typeof window!=="undefined" && window.speechSynthesis){
  try{ speechSynthesis.getVoices(); speechSynthesis.onvoiceschanged=()=>{ speechSynthesis.getVoices(); }; }catch(e){}
}
refreshAISpeechStatus();

// （原独立的 Sreyun 对话已并入上方统一的「AI 对话」——单窗口即走 Sreyun Agent。）

// 终端会话管理 + 回放 + 旁观
safeAddEventListener("termSessionsBtn", "click", openTerminalSessions);
// 终端会话搜索
function onTermSessionSearch(e) {
  TERM_SEARCH = (e && e.target && e.target.value) || "";
  renderTerminalSessions(LAST_TERM_SESSIONS);
}
safeAddEventListener("termSessionSearch", "input", onTermSessionSearch);
safeAddEventListener("termSessionSearch", "search", onTermSessionSearch);
safeAddEventListener("replayPlayBtn", "click", () => { if (REPLAY && REPLAY.playing) pauseReplay(); else playReplay(); });
safeAddEventListener("replayProgressBg", "click", e => {
  const rect = e.currentTarget.getBoundingClientRect();
  const progress = (e.clientX - rect.left) / rect.width;
  seekReplay(Math.max(0, Math.min(1, progress)));
});
document.querySelectorAll(".replay-speed-btn").forEach(btn => {
  btn.addEventListener("click", () => setReplaySpeed(parseFloat(btn.dataset.speed)));
});

