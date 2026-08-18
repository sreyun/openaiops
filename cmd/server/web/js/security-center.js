/* security-center.js — 安全中心：AI 工具审计 + 审计外发 + OIDC / 多提供商 SSO */

function setCfgStatus(el, text, kind) {
  if (!el) return;
  el.textContent = text || "";
  el.classList.remove("ok", "err");
  if (kind) el.classList.add(kind);
}

function parseJSONMap(raw, msgEl, errKey) {
  const t = (raw || "").trim();
  if (!t) return {};
  try { return JSON.parse(t); } catch (e) {
    setCfgStatus(msgEl, I18N.t(errKey || "sec.sso_json_invalid", "JSON 无效"), "err");
    return null;
  }
}

function defaultSSOCallback(provider) {
  const origin = (typeof location !== "undefined" && location.origin) ? location.origin : "";
  return origin + "/api/v1/auth/" + provider + "/callback";
}

function refreshSSOCallbackHints() {
  const map = {
    oidc: "ssoCbOidc",
    feishu: "ssoCbFeishu",
    dingtalk: "ssoCbDing",
    wechat: "ssoCbWx",
    wecom: "ssoCbWecom",
  };
  Object.keys(map).forEach(p => {
    const el = $(map[p]);
    if (el) el.textContent = defaultSSOCallback(p);
  });
}

function switchSSOProviderTab(tab) {
  const id = tab || "oidc";
  document.querySelectorAll("#ssoProviderTabs .tab").forEach(b => b.classList.toggle("active", b.dataset.ssoTab === id));
  document.querySelectorAll("#oidcPanel .tab-panel[id^='ssoTab-']").forEach(p => {
    p.classList.toggle("active", p.id === "ssoTab-" + id);
  });
}

function setSSOSaveStatus(text, kind) {
  document.querySelectorAll(".sso-save-status").forEach(el => setCfgStatus(el, text, kind));
}

function setSecretChip(chipId, hasSecret) {
  const chip = $(chipId);
  if (!chip) return;
  if (hasSecret) chip.removeAttribute("hidden");
  else chip.setAttribute("hidden", "");
}

function syncSSODenyWarn(enabledId, roleId, deptId, warnId) {
  const warn = $(warnId);
  if (!warn) return;
  const enabled = !!$(enabledId)?.checked;
  const role = ($(roleId)?.value || "").trim();
  let hasDept = false;
  if (deptId && $(deptId)) {
    const deptRaw = ($(deptId)?.value || "").trim();
    if (deptRaw) {
      try { hasDept = Object.keys(JSON.parse(deptRaw) || {}).length > 0; } catch (_) {}
    }
  }
  const show = enabled && !role && !hasDept;
  if (show) warn.removeAttribute("hidden");
  else warn.setAttribute("hidden", "");
}

function refreshAllSSODenyWarns() {
  syncSSODenyWarn("ssoFeishuEnabled", "ssoFeishuRole", "ssoFeishuDeptMap", "ssoFeishuDenyWarn");
  syncSSODenyWarn("ssoDingEnabled", "ssoDingRole", "ssoDingDeptMap", "ssoDingDenyWarn");
  syncSSODenyWarn("ssoWxEnabled", "ssoWxRole", null, "ssoWxDenyWarn");
  syncSSODenyWarn("ssoWecomEnabled", "ssoWecomRole", null, "ssoWecomDenyWarn");
}

async function copySSOCallback(provider) {
  const url = defaultSSOCallback(provider);
  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(url);
    } else {
      const ta = document.createElement("textarea");
      ta.value = url; document.body.appendChild(ta); ta.select();
      document.execCommand("copy"); document.body.removeChild(ta);
    }
    if (typeof toast === "function") toast(I18N.t("sec.sso_copied", "回调 URL 已复制"), "ok");
  } catch (e) {
    if (typeof toast === "function") toast(String(e), "err");
  }
}

async function loadAIToolAudit() {
  const el = $("aiToolAuditList");
  if (!el) return;
  el.innerHTML = `<div class="hint">${esc(I18N.t("sec.loading", "加载中…"))}</div>`;
  try {
    const rows = await fetch(`${API}/ai/tool-audit`, { credentials: "same-origin" }).then(r => r.json());
    if (!Array.isArray(rows) || !rows.length) {
      el.innerHTML = `<div class="hint">${esc(I18N.t("sec.ai_tools_empty", "暂无写工具调用记录。高危动作（如 run_python_action）会在此留证。"))}</div>`;
      return;
    }
    el.innerHTML = `<div class="cfg-table-wrap"><table class="data-table" style="width:100%"><thead><tr>
      <th>${esc(I18N.t("sec.col_time", "时间"))}</th>
      <th>${esc(I18N.t("sec.col_actor", "操作者"))}</th>
      <th>${esc(I18N.t("sec.col_tool", "工具"))}</th>
      <th>${esc(I18N.t("sec.col_action", "动作"))}</th>
      <th>${esc(I18N.t("sec.col_host", "主机"))}</th>
      <th>${esc(I18N.t("sec.col_status", "状态"))}</th>
      <th>${esc(I18N.t("sec.col_detail", "详情"))}</th>
    </tr></thead><tbody>${rows.map(e => {
      const ts = e.timestamp ? new Date(e.timestamp * 1000).toLocaleString() : "—";
      const st = e.blocked
        ? `<span class="tag warn">${esc(I18N.t("sec.status_blocked", "已阻断"))}</span>`
        : (e.approved
          ? `<span class="tag ok">${esc(I18N.t("sec.status_done", "已执行"))}</span>`
          : `<span class="tag">${esc(I18N.t("sec.status_pending", "待审"))}</span>`);
      return `<tr><td>${esc(ts)}</td><td>${esc(e.actor || "")}</td><td>${esc(e.tool || "")}</td><td>${esc(e.action || "")}</td>
        <td>${esc((typeof HostPicker!=="undefined"&&e.host_id)?(HostPicker.hostTitle({id:e.host_id,hostname:e.hostname,ip:e.ip||e.host_ip})||"未知主机"):(e.hostname||"未知主机"))}</td><td>${st}</td><td class="sec-detail">${esc((e.detail || "").replace(/\bhermes(?:\s+agent)?\b/gi,"智能运维服务"))}</td></tr>`;
    }).join("")}</tbody></table></div>`;
  } catch (e) {
    el.innerHTML = `<div class="hint">${esc(I18N.t("sec.load_failed", "加载失败"))}：${esc(String(e))}</div>`;
  }
}

async function loadAuditExportForm() {
  const msg = $("auditExportMsg");
  try {
    const c = await fetch(`${API}/audit-export`, { credentials: "same-origin" }).then(r => r.json());
    if ($("auditExportEnabled")) $("auditExportEnabled").checked = !!c.enabled;
    if ($("auditExportWebhook")) $("auditExportWebhook").value = c.webhook_url || "";
    if ($("auditExportSyslog")) $("auditExportSyslog").value = c.syslog_addr || "";
    if ($("auditExportFormat")) $("auditExportFormat").value = c.format || "json";
    setCfgStatus(msg, "", null);
  } catch (e) {
    setCfgStatus(msg, I18N.t("sec.export_load_failed", "加载失败（需管理员）"), "err");
  }
}

function fillSSOProvider(prefix, p, chipId) {
  p = p || {};
  if ($(prefix + "Enabled")) $(prefix + "Enabled").checked = !!p.enabled;
  if ($(prefix + "AppID")) $(prefix + "AppID").value = p.app_id || "";
  if ($(prefix + "AgentID")) $(prefix + "AgentID").value = p.agent_id || "";
  // Never put masked **** into the password field — keep empty + chip.
  if ($(prefix + "Secret")) $(prefix + "Secret").value = "";
  setSecretChip(chipId, !!(p.app_secret));
  if ($(prefix + "Redirect")) $(prefix + "Redirect").value = p.redirect_url || "";
  if ($(prefix + "Role")) $(prefix + "Role").value = p.default_role || "";
  if ($(prefix + "Auto")) $(prefix + "Auto").checked = p.auto_create !== false;
  if ($(prefix + "DeptMap")) $(prefix + "DeptMap").value = p.dept_role_map ? JSON.stringify(p.dept_role_map, null, 2) : "";
}

async function loadOIDCForm() {
  const msg = $("oidcMsg");
  refreshSSOCallbackHints();
  try {
    const c = await fetch(`${API}/auth/oidc/config`, { credentials: "same-origin" }).then(r => r.json());
    if ($("oidcEnabled")) $("oidcEnabled").checked = !!c.enabled;
    if ($("oidcIssuer")) $("oidcIssuer").value = c.issuer || "";
    if ($("oidcClientID")) $("oidcClientID").value = c.client_id || "";
    if ($("oidcClientSecret")) $("oidcClientSecret").value = "";
    setSecretChip("oidcSecretChip", !!(c.client_secret));
    if ($("oidcRedirect")) $("oidcRedirect").value = c.redirect_url || "";
    if ($("oidcGroupClaim")) $("oidcGroupClaim").value = c.group_claim || "groups";
    if ($("oidcGroupMap")) $("oidcGroupMap").value = c.group_role_map ? JSON.stringify(c.group_role_map, null, 2) : "";
    if ($("oidcDefaultRole")) $("oidcDefaultRole").value = c.default_role || "";
    if ($("oidcAutoCreate")) $("oidcAutoCreate").checked = c.auto_create !== false;
    window._oidcScopes = (c.scopes || "").trim() || "openid profile email groups";
    setCfgStatus(msg, "", null);
  } catch (_) {
    setCfgStatus(msg, I18N.t("sec.oidc_load_failed", "加载失败（需管理员）"), "err");
  }
  await loadSSOForm();
}

async function loadSSOForm() {
  refreshSSOCallbackHints();
  try {
    const c = await fetch(`${API}/auth/sso/config`, { credentials: "same-origin" }).then(r => r.json());
    fillSSOProvider("ssoFeishu", c.feishu, "ssoFeishuSecretChip");
    fillSSOProvider("ssoDing", c.dingtalk, "ssoDingSecretChip");
    fillSSOProvider("ssoWx", c.wechat, "ssoWxSecretChip");
    fillSSOProvider("ssoWecom", c.wecom, "ssoWecomSecretChip");
    refreshAllSSODenyWarns();
    setSSOSaveStatus("", null);
    updateSSOPreviewBar();
  } catch (_) {
    setSSOSaveStatus(I18N.t("sec.sso_load_failed", "加载 OAuth SSO 失败（需管理员）"), "err");
  }
}

function updateSSOPreviewBar() {
  const bar = $("ssoPreviewBar");
  if (!bar) return;
  const n = [
    $("oidcEnabled")?.checked && ($("oidcIssuer")?.value || "").trim() && ($("oidcClientID")?.value || "").trim(),
    $("ssoFeishuEnabled")?.checked && ($("ssoFeishuAppID")?.value || "").trim(),
    $("ssoDingEnabled")?.checked && ($("ssoDingAppID")?.value || "").trim(),
    $("ssoWxEnabled")?.checked && ($("ssoWxAppID")?.value || "").trim(),
    $("ssoWecomEnabled")?.checked && ($("ssoWecomAppID")?.value || "").trim() && ($("ssoWecomAgentID")?.value || "").trim(),
  ].filter(Boolean).length;
  bar.textContent = n
    ? I18N.t("sec.sso_preview_n", "登录页预计显示 {n} 个第三方入口（保存并刷新登录页后生效）").replace("{n}", String(n))
    : I18N.t("sec.sso_preview_none", "当前未启用任何第三方登录；登录页仅显示账号密码。");
}

async function saveOIDCConfig() {
  if (typeof isAdmin === "function" && !isAdmin()) {
    toast(I18N.t("toast.admin_only", "仅管理员可操作"), "err");
    return;
  }
  const msg = $("oidcMsg");
  let groupMap = {};
  const raw = ($("oidcGroupMap")?.value || "").trim();
  if (raw) {
    try { groupMap = JSON.parse(raw); } catch (e) {
      setCfgStatus(msg, I18N.t("sec.oidc_json_invalid", "组映射 JSON 无效"), "err");
      return;
    }
  }
  const body = {
    enabled: !!$("oidcEnabled")?.checked,
    issuer: ($("oidcIssuer")?.value || "").trim(),
    client_id: ($("oidcClientID")?.value || "").trim(),
    client_secret: ($("oidcClientSecret")?.value || "").trim(),
    redirect_url: ($("oidcRedirect")?.value || "").trim(),
    group_claim: ($("oidcGroupClaim")?.value || "").trim() || "groups",
    group_role_map: groupMap,
    default_role: ($("oidcDefaultRole")?.value || "").trim(),
    auto_create: !!$("oidcAutoCreate")?.checked,
    scopes: (window._oidcScopes || "openid profile email groups").trim(),
  };
  const run = async () => {
    try {
      const r = await fetch(`${API}/auth/oidc/config`, {
        method: "POST", credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const j = await r.json().catch(() => ({}));
      if (r.ok) {
        setCfgStatus(msg, I18N.t("sec.oidc_saved", "OIDC 配置已保存"), "ok");
        if (typeof toast === "function") toast(I18N.t("sec.oidc_saved", "OIDC 配置已保存"), "ok");
        if ($("oidcClientSecret")) $("oidcClientSecret").value = "";
        setSecretChip("oidcSecretChip", true);
        updateSSOPreviewBar();
        loadSSOLoginButtons();
      } else {
        setCfgStatus(msg, j.error || I18N.t("toast.save_failed", "保存失败"), "err");
      }
    } catch (e) {
      setCfgStatus(msg, String(e), "err");
    }
  };
  if (typeof withLoading === "function") await withLoading("oidcSaveBtn", run);
  else await run();
}

function collectSSOProvider(prefix) {
  const deptEl = $(prefix + "DeptMap");
  let dept = {};
  if (deptEl) {
    const t = (deptEl.value || "").trim();
    if (t) {
      try { dept = JSON.parse(t); } catch (e) {
        setSSOSaveStatus(I18N.t("sec.sso_json_invalid", "JSON 无效"), "err");
        return null;
      }
    }
  }
  const out = {
    enabled: !!$(prefix + "Enabled")?.checked,
    app_id: ($(prefix + "AppID")?.value || "").trim(),
    app_secret: ($(prefix + "Secret")?.value || "").trim(),
    redirect_url: ($(prefix + "Redirect")?.value || "").trim(),
    default_role: ($(prefix + "Role")?.value || "").trim(),
    auto_create: !!$(prefix + "Auto")?.checked,
    dept_role_map: dept,
  };
  if ($(prefix + "AgentID")) out.agent_id = ($(prefix + "AgentID").value || "").trim();
  return out;
}

async function saveSSOConfig() {
  if (typeof isAdmin === "function" && !isAdmin()) {
    toast(I18N.t("toast.admin_only", "仅管理员可操作"), "err");
    return;
  }
  const feishu = collectSSOProvider("ssoFeishu");
  const dingtalk = collectSSOProvider("ssoDing");
  const wechat = collectSSOProvider("ssoWx");
  const wecom = collectSSOProvider("ssoWecom");
  if (!feishu || !dingtalk || !wechat || !wecom) return;
  const body = { feishu, dingtalk, wechat, wecom };
  const run = async () => {
    try {
      const r = await fetch(`${API}/auth/sso/config`, {
        method: "POST", credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const j = await r.json().catch(() => ({}));
      if (r.ok) {
        setSSOSaveStatus(I18N.t("sec.sso_saved", "OAuth SSO 配置已保存"), "ok");
        if (typeof toast === "function") toast(I18N.t("sec.sso_saved", "OAuth SSO 配置已保存"), "ok");
        ["ssoFeishu", "ssoDing", "ssoWx", "ssoWecom"].forEach(p => { if ($(p + "Secret")) $(p + "Secret").value = ""; });
        await loadSSOForm();
        loadSSOLoginButtons();
      } else {
        setSSOSaveStatus(j.error || I18N.t("toast.save_failed", "保存失败"), "err");
      }
    } catch (e) {
      setSSOSaveStatus(String(e), "err");
    }
  };
  const btn = document.querySelector("#oidcPanel .tab-panel.active .sso-oauth-save") || document.querySelector(".sso-oauth-save");
  if (typeof withLoading === "function" && btn && btn.id) await withLoading(btn.id, run);
  else {
    if (btn) btn.disabled = true;
    try { await run(); } finally { if (btn) btn.disabled = false; }
  }
}

async function saveAuditExport() {
  if (typeof isAdmin === "function" && !isAdmin()) {
    toast(I18N.t("toast.admin_only", "仅管理员可操作"), "err");
    return;
  }
  const msg = $("auditExportMsg");
  const enabled = !!$("auditExportEnabled")?.checked;
  const webhook = ($("auditExportWebhook")?.value || "").trim();
  const syslog = ($("auditExportSyslog")?.value || "").trim();
  if (enabled && !webhook && !syslog) {
    setCfgStatus(msg, I18N.t("sec.export_need_target", "启用前请至少填写 Webhook 或 Syslog 地址"), "err");
    return;
  }
  const body = {
    enabled,
    webhook_url: webhook,
    syslog_addr: syslog,
    format: ($("auditExportFormat")?.value || "json"),
  };
  const run = async () => {
    try {
      const r = await fetch(`${API}/audit-export`, {
        method: "POST", credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const j = await r.json().catch(() => ({}));
      if (r.ok) {
        setCfgStatus(msg, I18N.t("sec.export_saved", "审计外发配置已保存"), "ok");
        if (typeof toast === "function") toast(I18N.t("sec.export_saved", "审计外发配置已保存"), "ok");
      } else {
        setCfgStatus(msg, j.error || I18N.t("toast.save_failed", "保存失败"), "err");
      }
    } catch (e) {
      setCfgStatus(msg, String(e), "err");
    }
  };
  if (typeof withLoading === "function") await withLoading("auditExportSaveBtn", run);
  else await run();
}

function ssoProviderLabel(p) {
  if (p.id === "oidc") return I18N.t("login.oidc_sso", "企业 SSO 登录");
  if (p.id === "feishu") return I18N.t("login.feishu_sso", "飞书登录");
  if (p.id === "dingtalk") return I18N.t("login.dingtalk_sso", "钉钉登录");
  if (p.id === "wechat") return I18N.t("login.wechat_sso", "微信扫码登录");
  if (p.id === "wecom") return I18N.t("login.wecom_sso", "企业微信登录");
  return p.name || p.id;
}

function ssoProviderIcon(id) {
  const map = { oidc: "OIDC", feishu: "飞", dingtalk: "钉", wechat: "微", wecom: "企" };
  return map[id] || "·";
}

function ssoErrorMessage(code, provider) {
  const provName = ({
    oidc: I18N.t("login.oidc_sso", "企业 SSO"),
    feishu: I18N.t("login.feishu_sso", "飞书"),
    dingtalk: I18N.t("login.dingtalk_sso", "钉钉"),
    wechat: I18N.t("login.wechat_sso", "微信"),
    wecom: I18N.t("login.wecom_sso", "企业微信"),
  })[provider] || provider || I18N.t("login.sso_generic", "第三方登录");
  const keys = {
    denied: "login.sso_error_denied",
    state: "login.sso_error_state",
    no_role: "login.sso_error_no_role",
    no_user: "login.sso_error_no_user",
    exchange: "login.sso_error_exchange",
    discovery: "login.sso_error_discovery",
    disabled: "login.sso_error_disabled",
    conflict: "login.sso_error_conflict",
    bad_profile: "login.sso_error_bad_profile",
    provision: "login.sso_error_provision",
    idp: "login.sso_error_idp",
    config: "login.sso_error_config",
    id_token: "login.sso_error_id_token",
    bind_auth: "login.sso_error_bind_auth",
  };
  const fallbacks = {
    denied: "已取消授权或被身份提供商拒绝",
    state: "登录会话已过期，请重新点击第三方登录",
    no_role: "未匹配到可用角色，请联系管理员配置默认角色或部门映射",
    no_user: "本地账号未开通且未启用自动建号",
    exchange: "与身份提供商交换令牌失败，请检查 App 配置与回调地址",
    discovery: "无法发现 OIDC Issuer，请检查 Issuer 地址",
    disabled: "该登录方式未启用",
    conflict: "该第三方身份已绑定其他账号",
    bad_profile: "身份提供商未返回可用的用户标识",
    provision: "创建本地用户失败",
    idp: "身份提供商返回错误",
    config: "登录应用配置不完整",
    id_token: "ID Token 校验失败（签名/iss/aud/nonce）",
    bind_auth: "请先登录本地账号后再绑定第三方身份",
  };
  const key = keys[code] || "login.sso_error_unknown";
  const fb = fallbacks[code] || "第三方登录失败，请重试或使用账号密码";
  return I18N.t(key, fb).replace("{provider}", provName);
}

function applySSOErrorFromURL() {
  try {
    const q = new URLSearchParams(location.search || "");
    const bound = q.get("sso_bound");
    const code = q.get("sso_error");
    const provider = q.get("sso_provider") || "";
    const clearQ = () => {
      if (history.replaceState) history.replaceState({}, "", location.pathname + (location.hash || ""));
    };
    if (bound === "1") {
      if (typeof toast === "function") {
        toast(I18N.t("profile.sso_bound_ok", "第三方账号绑定成功"), "ok");
      }
      clearQ();
      return;
    }
    if (!code) return;
    const msg = ssoErrorMessage(code, provider);
    // Bind-flow errors (or already logged-in): toast instead of forcing login card.
    const loggedIn = typeof CUR_ROLE !== "undefined" && !!CUR_ROLE;
    if (code === "bind_auth" || code === "conflict" || loggedIn) {
      if (typeof toast === "function") toast(msg, "err");
      clearQ();
      return;
    }
    const el = $("loginErr");
    if (el) {
      el.textContent = msg;
      try { el.focus?.(); } catch (_) {}
    }
    const lv = $("loginView");
    if (lv) lv.classList.add("show");
    clearQ();
  } catch (_) {}
}

async function loadProfileSSOBindings() {
  const box = $("pfSSOList");
  const err = $("pfSSOErr");
  if (!box) return;
  if (err) { err.style.display = "none"; err.textContent = ""; }
  box.innerHTML = `<div class="hint">${esc(I18N.t("sec.loading", "加载中…"))}</div>`;
  try {
    const j = await fetch(`${API}/auth/sso/identities`, { credentials: "same-origin" }).then(r => r.json());
    const providers = j.providers || [];
    const identities = j.identities || [];
    const subByProv = {};
    identities.forEach(id => { subByProv[String(id.provider || "").toLowerCase()] = id.subject || ""; });
    if (!providers.length) {
      box.innerHTML = `<div class="hint">${esc(I18N.t("profile.sso_none_enabled", "管理员尚未启用任何第三方登录；启用后可在此绑定到当前账号。"))}</div>`;
      return;
    }
    box.innerHTML = providers.map(p => {
      const bound = !!p.bound;
      const sub = subByProv[p.id] || "";
      const actions = bound
        ? `<button type="button" class="btn sm danger" data-sso-unbind="${esc(p.id)}">${esc(I18N.t("profile.sso_unbind", "解除绑定"))}</button>`
        : `<a class="btn sm primary" href="${esc(p.bind_url || "#")}">${esc(I18N.t("profile.sso_bind", "绑定到当前账号"))}</a>`;
      return `<div class="pf-sso-row" data-sso-prov="${esc(p.id)}">
        <div class="pf-sso-meta">
          <span class="pf-sso-name">${esc(ssoProviderLabel(p))}</span>
          <span class="pf-sso-sub">${bound ? esc(I18N.t("profile.sso_bound_as", "已绑定：{sub}").replace("{sub}", sub || p.id)) : esc(I18N.t("profile.sso_unbound", "未绑定"))}</span>
        </div>
        <div class="pf-sso-actions">${actions}</div>
      </div>`;
    }).join("");
    box.querySelectorAll("[data-sso-unbind]").forEach(btn => {
      btn.addEventListener("click", async () => {
        const prov = btn.getAttribute("data-sso-unbind");
        if (!prov) return;
        if (!confirm(I18N.t("profile.sso_unbind_confirm", "确定解除该第三方绑定？"))) return;
        try {
          const r = await fetch(`${API}/auth/sso/identities/${encodeURIComponent(prov)}`, {
            method: "DELETE", credentials: "same-origin",
          });
          const body = await r.json().catch(() => ({}));
          if (!r.ok) throw new Error(body.error || I18N.t("toast.update_failed", "操作失败"));
          toast(I18N.t("profile.sso_unbound_ok", "已解除绑定"), "ok");
          await loadProfileSSOBindings();
        } catch (e) {
          if (err) { err.textContent = String(e.message || e); err.style.display = "block"; }
          else toast(String(e.message || e), "err");
        }
      });
    });
  } catch (e) {
    box.innerHTML = `<div class="hint">${esc(I18N.t("sec.load_failed", "加载失败"))}：${esc(String(e))}</div>`;
  }
}

async function loadSSOLoginButtons() {
  const box = $("ssoLoginBtns");
  const divider = $("ssoLoginDivider");
  if (!box) return;
  try {
    const j = await fetch(`${API}/auth/sso/info`).then(r => r.json());
    const list = (j && j.providers) || [];
    if (!list.length) {
      box.innerHTML = "";
      box.hidden = true;
      if (divider) divider.hidden = true;
      return;
    }
    box.innerHTML = list.map(p =>
      `<a class="btn block sso-login-btn" data-sso="${esc(p.id)}" href="${esc(p.login_url)}">` +
      `<span class="sso-ic" aria-hidden="true">${esc(ssoProviderIcon(p.id))}</span>` +
      `<span>${esc(ssoProviderLabel(p))}</span></a>`
    ).join("");
    box.hidden = false;
    if (divider) divider.hidden = false;
  } catch (_) {
    box.innerHTML = "";
    box.hidden = true;
    if (divider) divider.hidden = true;
  }
}

async function loadOIDCLoginButton() { return loadSSOLoginButtons(); }

window._pageRenderers = window._pageRenderers || {};
window._pageRenderers["ai-tool-audit"] = loadAIToolAudit;
window._pageRenderers["audit-export"] = loadAuditExportForm;
window._pageRenderers["oidc"] = loadOIDCForm;

document.addEventListener("DOMContentLoaded", () => {
  const saveBtn = $("auditExportSaveBtn");
  if (saveBtn) saveBtn.addEventListener("click", saveAuditExport);
  const oidcBtn = $("oidcSaveBtn");
  if (oidcBtn) oidcBtn.addEventListener("click", saveOIDCConfig);
  document.querySelectorAll(".sso-oauth-save").forEach(b => b.addEventListener("click", saveSSOConfig));
  document.querySelectorAll("#ssoProviderTabs .tab").forEach(b => {
    b.addEventListener("click", () => switchSSOProviderTab(b.dataset.ssoTab));
  });
  document.querySelectorAll("[data-copy-cb]").forEach(b => {
    b.addEventListener("click", () => copySSOCallback(b.getAttribute("data-copy-cb")));
  });
  ["ssoFeishuEnabled", "ssoFeishuRole", "ssoFeishuDeptMap",
    "ssoDingEnabled", "ssoDingRole", "ssoDingDeptMap",
    "ssoWxEnabled", "ssoWxRole",
    "ssoWecomEnabled", "ssoWecomRole", "ssoWecomAgentID", "ssoWecomAppID",
    "oidcEnabled", "oidcIssuer", "oidcClientID"].forEach(id => {
    const el = $(id);
    if (!el) return;
    el.addEventListener("change", () => { refreshAllSSODenyWarns(); updateSSOPreviewBar(); });
    el.addEventListener("input", () => { refreshAllSSODenyWarns(); updateSSOPreviewBar(); });
  });
  refreshSSOCallbackHints();
  switchSSOProviderTab("oidc");
  applySSOErrorFromURL();
  loadSSOLoginButtons();
});
