/* ---------- 告警设置 ---------- */
async function openSettings() {
  if (!isAdmin()) { toast(I18N.t("toast.admin_only", "仅管理员可操作"), "err"); return; }
  try {
    const c = await fetch(`${API}/config`).then(r => r.json());
    const t = c.thresholds || {};
    $("alertsEnabled").checked = !!c.alerts_enabled;
    $("feishuEnabled").checked = !!(c.feishu && c.feishu.enabled);
    $("feishuWebhook").value = (c.feishu && c.feishu.webhook) || "";
    $("dingEnabled").checked = !!(c.dingtalk && c.dingtalk.enabled);
    $("dingWebhook").value = (c.dingtalk && c.dingtalk.webhook) || "";
    $("dingSecret").value = (c.dingtalk && c.dingtalk.secret) || "";
    // Custom webhook
    const cw = c.custom_webhook || {};
    $("customWebhookEnabled").checked = !!cw.enabled;
    $("customWebhookURL").value = cw.url || "";
    $("customWebhookMethod").value = cw.method || "POST";
    $("customWebhookContentType").value = cw.content_type || "application/json";
    $("customWebhookHeaders").value = cw.headers || "";
    $("customWebhookBodyTemplate").value = cw.body_template || "";
    // SMTP email config
    const s = c.smtp || {};
    $("smtpEnabled").checked = !!s.smtp_enabled;
    $("smtpHost").value = s.smtp_host || "";
    $("smtpPort").value = s.smtp_port || "";
    $("smtpUsername").value = s.smtp_username || "";
    $("smtpPassword").value = s.smtp_password || "";
    $("smtpFromName").value = s.smtp_from_name || "";
    $("smtpTLS").checked = !!s.smtp_use_tls;
    // SMS config
    const sms = c.sms || {};
    $("smsEnabled").checked = !!sms.enabled;
    $("smsProvider").value = sms.provider || "aliyun";
    $("smsAccessKey").value = sms.access_key || "";
    $("smsSecretKey").value = sms.secret_key || "";
    $("smsSignName").value = sms.sign_name || "";
    $("smsTemplateCode").value = sms.template_code || "";
    $("smsTemplateParam").value = sms.template_param || "";
    $("smsAppId").value = sms.app_id || "";
    $("smsSender").value = sms.sender || "";
    $("smsRegion").value = sms.region || "";
    $("smsPhones").value = (sms.phones || []).join(",");
    // VoiceCall config
    const vc = c.voice_call || {};
    $("voiceCallEnabled").checked = !!vc.enabled;
    $("voiceCallProvider").value = vc.provider || "aliyun";
    $("voiceCallAccessKey").value = vc.access_key || "";
    $("voiceCallSecretKey").value = vc.secret_key || "";
    $("voiceCallCalledNumbers").value = (vc.called_numbers || []).join(",");
    $("voiceCallTtsCode").value = vc.tts_code || "";
    $("voiceCallTtsParam").value = vc.tts_param || "";
    $("voiceCallAppId").value = vc.app_id || "";
    $("voiceCallDisplayNbr").value = vc.display_nbr || "";
    $("voiceCallRegion").value = vc.region || "";
    updateSmsProviderFields();
    updateVoiceProviderFields();
    // Threshold display: treat 0 / null / undefined as "unset" → show the standard
    // default. The backend also backfills these zeros, so display and storage stay
    // consistent, and every metric always shows a sane standard threshold.
    const td = (v, def) => (v == null || v === 0 || isNaN(v)) ? def : v;
    $("cpuWarn").value = td(t.cpu_warn, 80); $("cpuCrit").value = td(t.cpu_crit, 95);
    $("memWarn").value = td(t.mem_warn, 85); $("memCrit").value = td(t.mem_crit, 95);
    $("diskWarn").value = td(t.disk_warn, 80); $("diskCrit").value = td(t.disk_crit, 90);
    $("diskioWarn").value = td(t.diskio_warn, 80); $("diskioCrit").value = td(t.diskio_crit, 95);
    $("iopsWarn").value = td(t.iops_warn, 50000); $("iopsCrit").value = td(t.iops_crit, 100000);
    $("gpuWarn").value = td(t.gpu_warn, 80); $("gpuCrit").value = td(t.gpu_crit, 95);
    $("loadWarn").value = td(t.load_warn, 4.0); $("loadCrit").value = td(t.load_crit, 8.0);
    $("procWarn").value = td(t.proc_warn, 0.5);
    $("offlineSec").value = td(t.offline_after_sec, 60);
    // 拨测监控
    $("checkPingLossWarn").value = td(t.check_ping_loss_warn, 10); $("checkPingLossCrit").value = td(t.check_ping_loss_crit, 30);
    $("checkPingLatencyWarn").value = td(t.check_ping_latency_warn, 100); $("checkPingLatencyCrit").value = td(t.check_ping_latency_crit, 500);
    $("checkTCPTimeoutWarn").value = td(t.check_tcp_timeout_warn, 1000); $("checkTCPTimeoutCrit").value = td(t.check_tcp_timeout_crit, 5000);
    $("checkHTTPRespWarn").value = td(t.check_http_resp_warn, 1000); $("checkHTTPRespCrit").value = td(t.check_http_resp_crit, 5000);
    $("checkHTTPStatusWarn").value = td(t.check_http_status_warn, 1); $("checkHTTPStatusCrit").value = td(t.check_http_status_crit, 5);
    $("checkProcFailWarn").value = td(t.check_proc_fail_warn, 1); $("checkProcFailCrit").value = td(t.check_proc_fail_crit, 3);
    $("checkUDPTimeoutWarn").value = td(t.check_udp_timeout_warn, 1000); $("checkUDPTimeoutCrit").value = td(t.check_udp_timeout_crit, 5000);
    $("checkDNSTimeoutWarn").value = td(t.check_dns_timeout_warn, 500); $("checkDNSTimeoutCrit").value = td(t.check_dns_timeout_crit, 2000);
    // API 业务监控
    $("apiAvailWarn").value = td(t.api_avail_warn, 99); $("apiAvailCrit").value = td(t.api_avail_crit, 95);
    $("apiAvgRespWarn").value = td(t.api_avg_resp_warn, 500); $("apiAvgRespCrit").value = td(t.api_avg_resp_crit, 2000);
    $("apiP95RespWarn").value = td(t.api_p95_resp_warn, 1000); $("apiP95RespCrit").value = td(t.api_p95_resp_crit, 5000);
    $("apiThroughputWarn").value = td(t.api_throughput_warn, 100); $("apiThroughputCrit").value = td(t.api_throughput_crit, 10);
    // 编排定时任务
    $("taskFailWarn").value = td(t.task_fail_warn, 1); $("taskFailCrit").value = td(t.task_fail_crit, 5);
    $("taskTimeoutWarn").value = td(t.task_timeout_warn, 60); $("taskTimeoutCrit").value = td(t.task_timeout_crit, 300);
    // 端口转发监控
    $("forwardConnWarn").value = td(t.forward_conn_warn, 200); $("forwardConnCrit").value = td(t.forward_conn_crit, 280);
    $("forwardBwWarn").value = td(t.forward_bw_warn, 80); $("forwardBwCrit").value = td(t.forward_bw_crit, 95);
    $("forwardErrWarn").value = td(t.forward_err_warn, 5); $("forwardErrCrit").value = td(t.forward_err_crit, 15);
    $("forwardLatWarn").value = td(t.forward_lat_warn, 1000); $("forwardLatCrit").value = td(t.forward_lat_crit, 5000);
    // SNMP 网络设备
    $("snmpIfUtilWarn").value = td(t.snmp_if_util_warn, 80); $("snmpIfUtilCrit").value = td(t.snmp_if_util_crit, 95);
    $("snmpIfErrWarn").value = td(t.snmp_if_err_warn, 1); $("snmpIfErrCrit").value = td(t.snmp_if_err_crit, 10);

    // Reset to first tab
    switchNotifyTab("tab-feishu");

    $("settingsMask").classList.add("show");
  } catch (e) { toast(I18N.t("toast.read_config_failed") + e, "err"); }
}

// ---- 告警阈值 Tab（已从「告警设置」弹窗独立出来，隶属「告警」模块）----
// 阈值输入框（同 ID）现位于 #view-thresholds。加载：拉全量配置回填字段；
// 保存：拉全量配置 → 仅覆盖 thresholds → 回存，从而不触碰 webhook/smtp 等其它设置
// （脱敏密钥原样回传，由后端按「掩码=保持原值」逻辑保留）。
let _thresholdPresetsCache = null;

function fillThresholdForm(t) {
  t = t || {};
  const td = (v, def) => (v == null || v === 0 || isNaN(v)) ? def : v;
  $("cpuWarn").value = td(t.cpu_warn, 80); $("cpuCrit").value = td(t.cpu_crit, 95);
  $("memWarn").value = td(t.mem_warn, 85); $("memCrit").value = td(t.mem_crit, 95);
  $("diskWarn").value = td(t.disk_warn, 80); $("diskCrit").value = td(t.disk_crit, 90);
  $("diskioWarn").value = td(t.diskio_warn, 80); $("diskioCrit").value = td(t.diskio_crit, 95);
  $("iopsWarn").value = td(t.iops_warn, 50000); $("iopsCrit").value = td(t.iops_crit, 100000);
  $("gpuWarn").value = td(t.gpu_warn, 80); $("gpuCrit").value = td(t.gpu_crit, 95);
  $("gpuTempWarn").value = td(t.gpu_temp_warn, 85); $("gpuTempCrit").value = td(t.gpu_temp_crit, 95);
  $("gpuMemWarn").value = td(t.gpu_mem_warn, 90); $("gpuMemCrit").value = td(t.gpu_mem_crit, 97);
  $("loadWarn").value = td(t.load_warn, 4.0); $("loadCrit").value = td(t.load_crit, 8.0);
  $("procWarn").value = td(t.proc_warn, 0.5);
  $("connWarn").value = td(t.conn_warn, 5000); $("connCrit").value = td(t.conn_crit, 10000);
  $("offlineSec").value = td(t.offline_after_sec, 60);
  $("checkPingLossWarn").value = td(t.check_ping_loss_warn, 10); $("checkPingLossCrit").value = td(t.check_ping_loss_crit, 30);
  $("checkPingLatencyWarn").value = td(t.check_ping_latency_warn, 100); $("checkPingLatencyCrit").value = td(t.check_ping_latency_crit, 500);
  $("checkTCPTimeoutWarn").value = td(t.check_tcp_timeout_warn, 1000); $("checkTCPTimeoutCrit").value = td(t.check_tcp_timeout_crit, 5000);
  $("checkHTTPRespWarn").value = td(t.check_http_resp_warn, 1000); $("checkHTTPRespCrit").value = td(t.check_http_resp_crit, 5000);
  $("checkHTTPStatusWarn").value = td(t.check_http_status_warn, 1); $("checkHTTPStatusCrit").value = td(t.check_http_status_crit, 5);
  $("checkProcFailWarn").value = td(t.check_proc_fail_warn, 1); $("checkProcFailCrit").value = td(t.check_proc_fail_crit, 3);
  $("checkUDPTimeoutWarn").value = td(t.check_udp_timeout_warn, 1000); $("checkUDPTimeoutCrit").value = td(t.check_udp_timeout_crit, 5000);
  $("checkDNSTimeoutWarn").value = td(t.check_dns_timeout_warn, 500); $("checkDNSTimeoutCrit").value = td(t.check_dns_timeout_crit, 2000);
  $("apiAvailWarn").value = td(t.api_avail_warn, 99); $("apiAvailCrit").value = td(t.api_avail_crit, 95);
  $("apiAvgRespWarn").value = td(t.api_avg_resp_warn, 500); $("apiAvgRespCrit").value = td(t.api_avg_resp_crit, 2000);
  $("apiP95RespWarn").value = td(t.api_p95_resp_warn, 1000); $("apiP95RespCrit").value = td(t.api_p95_resp_crit, 5000);
  $("apiThroughputWarn").value = td(t.api_throughput_warn, 100); $("apiThroughputCrit").value = td(t.api_throughput_crit, 10);
  $("taskFailWarn").value = td(t.task_fail_warn, 1); $("taskFailCrit").value = td(t.task_fail_crit, 5);
  $("taskTimeoutWarn").value = td(t.task_timeout_warn, 60); $("taskTimeoutCrit").value = td(t.task_timeout_crit, 300);
  $("forwardConnWarn").value = td(t.forward_conn_warn, 200); $("forwardConnCrit").value = td(t.forward_conn_crit, 280);
  $("forwardBwWarn").value = td(t.forward_bw_warn, 80); $("forwardBwCrit").value = td(t.forward_bw_crit, 95);
  $("forwardErrWarn").value = td(t.forward_err_warn, 5); $("forwardErrCrit").value = td(t.forward_err_crit, 15);
  $("forwardLatWarn").value = td(t.forward_lat_warn, 1000); $("forwardLatCrit").value = td(t.forward_lat_crit, 5000);
  $("snmpIfUtilWarn").value = td(t.snmp_if_util_warn, 80); $("snmpIfUtilCrit").value = td(t.snmp_if_util_crit, 95);
  $("snmpIfErrWarn").value = td(t.snmp_if_err_warn, 1); $("snmpIfErrCrit").value = td(t.snmp_if_err_crit, 10);
  if ($("netflowSurgeRatio")) $("netflowSurgeRatio").value = td(t.netflow_surge_ratio, 3);
  if ($("netflowSurgeMinMbps")) $("netflowSurgeMinMbps").value = td(t.netflow_surge_min_mbps, 1);
  if ($("netflowDropWarn")) $("netflowDropWarn").value = td(t.netflow_drop_warn, 100);
}

function collectThresholdsFromForm() {
  const num = id => parseFloat($(id).value) || 0;
  return {
    cpu_warn: num("cpuWarn"), cpu_crit: num("cpuCrit"),
    mem_warn: num("memWarn"), mem_crit: num("memCrit"),
    disk_warn: num("diskWarn"), disk_crit: num("diskCrit"),
    diskio_warn: num("diskioWarn"), diskio_crit: num("diskioCrit"),
    iops_warn: num("iopsWarn"), iops_crit: num("iopsCrit"),
    gpu_warn: num("gpuWarn"), gpu_crit: num("gpuCrit"),
    gpu_temp_warn: num("gpuTempWarn"), gpu_temp_crit: num("gpuTempCrit"),
    gpu_mem_warn: num("gpuMemWarn"), gpu_mem_crit: num("gpuMemCrit"),
    load_warn: num("loadWarn"), load_crit: num("loadCrit"),
    proc_warn: num("procWarn"),
    conn_warn: Math.round(num("connWarn")), conn_crit: Math.round(num("connCrit")),
    offline_after_sec: Math.round(num("offlineSec")),
    check_ping_loss_warn: num("checkPingLossWarn"), check_ping_loss_crit: num("checkPingLossCrit"),
    check_ping_latency_warn: num("checkPingLatencyWarn"), check_ping_latency_crit: num("checkPingLatencyCrit"),
    check_tcp_timeout_warn: num("checkTCPTimeoutWarn"), check_tcp_timeout_crit: num("checkTCPTimeoutCrit"),
    check_http_resp_warn: num("checkHTTPRespWarn"), check_http_resp_crit: num("checkHTTPRespCrit"),
    check_http_status_warn: Math.round(num("checkHTTPStatusWarn")), check_http_status_crit: Math.round(num("checkHTTPStatusCrit")),
    check_proc_fail_warn: Math.round(num("checkProcFailWarn")), check_proc_fail_crit: Math.round(num("checkProcFailCrit")),
    check_udp_timeout_warn: num("checkUDPTimeoutWarn"), check_udp_timeout_crit: num("checkUDPTimeoutCrit"),
    check_dns_timeout_warn: num("checkDNSTimeoutWarn"), check_dns_timeout_crit: num("checkDNSTimeoutCrit"),
    api_avail_warn: num("apiAvailWarn"), api_avail_crit: num("apiAvailCrit"),
    api_avg_resp_warn: num("apiAvgRespWarn"), api_avg_resp_crit: num("apiAvgRespCrit"),
    api_p95_resp_warn: num("apiP95RespWarn"), api_p95_resp_crit: num("apiP95RespCrit"),
    api_throughput_warn: num("apiThroughputWarn"), api_throughput_crit: num("apiThroughputCrit"),
    task_fail_warn: Math.round(num("taskFailWarn")), task_fail_crit: Math.round(num("taskFailCrit")),
    task_timeout_warn: num("taskTimeoutWarn"), task_timeout_crit: num("taskTimeoutCrit"),
    forward_conn_warn: Math.round(num("forwardConnWarn")), forward_conn_crit: Math.round(num("forwardConnCrit")),
    forward_bw_warn: num("forwardBwWarn"), forward_bw_crit: num("forwardBwCrit"),
    forward_err_warn: num("forwardErrWarn"), forward_err_crit: num("forwardErrCrit"),
    forward_lat_warn: num("forwardLatWarn"), forward_lat_crit: num("forwardLatCrit"),
    snmp_if_util_warn: num("snmpIfUtilWarn"), snmp_if_util_crit: num("snmpIfUtilCrit"),
    snmp_if_err_warn: num("snmpIfErrWarn"), snmp_if_err_crit: num("snmpIfErrCrit"),
    netflow_surge_ratio: num("netflowSurgeRatio"), netflow_surge_min_mbps: num("netflowSurgeMinMbps"),
    netflow_drop_warn: Math.round(num("netflowDropWarn"))
  };
}

function markThresholdPresetActive(key) {
  document.querySelectorAll(".threshold-preset-card").forEach(el => {
    const on = !!key && el.dataset.preset === key;
    el.classList.toggle("active", on);
    el.setAttribute("aria-pressed", on ? "true" : "false");
  });
}

function thresholdsRoughlyEqual(a, b) {
  if (!a || !b) return false;
  const keys = Object.keys(b);
  for (const k of keys) {
    const av = Number(a[k]), bv = Number(b[k]);
    if (!Number.isFinite(av) || !Number.isFinite(bv)) return false;
    if (Math.abs(av - bv) > 1e-6) return false;
  }
  return true;
}

async function loadThresholdPresets() {
  if (_thresholdPresetsCache) return _thresholdPresetsCache;
  const r = await fetch(`${API}/config/threshold-presets`);
  if (!r.ok) throw new Error("HTTP " + r.status);
  _thresholdPresetsCache = await r.json();
  return _thresholdPresetsCache;
}

async function syncActiveThresholdPreset() {
  try {
    const presets = await loadThresholdPresets();
    const cur = collectThresholdsFromForm();
    let match = "";
    for (const key of ["conservative", "standard", "relaxed"]) {
      if (thresholdsRoughlyEqual(cur, presets[key])) { match = key; break; }
    }
    markThresholdPresetActive(match);
  } catch (_) { /* ignore */ }
}

async function applyThresholdPreset(key) {
  if (!isAdmin()) { toast(I18N.t("toast.admin_only", "仅管理员可操作"), "err"); return; }
  const labels = {
    conservative: I18N.t("settings.preset_conservative", "严格敏感"),
    standard: I18N.t("settings.preset_standard", "标准均衡"),
    relaxed: I18N.t("settings.preset_relaxed", "宽松低噪")
  };
  const name = labels[key] || key;
  if (!confirm(I18N.t("settings.preset_apply_confirm", "将应用「{0}」档位到全部阈值并立即保存生效，是否继续？").replace("{0}", name))) {
    return;
  }
  const card = document.querySelector(`.threshold-preset-card[data-preset="${key}"]`);
  try {
    if (card) card.classList.add("applying");
    const presets = await loadThresholdPresets();
    const t = presets[key];
    if (!t) { toast(I18N.t("toast.read_config_failed", "读取配置失败"), "err"); return; }
    fillThresholdForm(t);
    markThresholdPresetActive(key);
    const ok = await saveThresholds(true);
    if (ok) toast(I18N.t("settings.preset_applied", "已应用「{0}」档位并保存").replace("{0}", name), "ok");
    else await syncActiveThresholdPreset();
  } catch (e) {
    toast(I18N.t("toast.save_failed2") + e, "err");
    await syncActiveThresholdPreset();
  } finally {
    if (card) card.classList.remove("applying");
  }
}

async function loadThresholds() {
  try {
    const c = await fetch(`${API}/config`).then(r => r.json());
    fillThresholdForm(c.thresholds || {});
    await syncActiveThresholdPreset();
  } catch (e) { toast(I18N.t("toast.read_config_failed") + e, "err"); }
  const editable = isAdmin();
  document.querySelectorAll("#view-thresholds input").forEach(el => { el.disabled = !editable; });
  document.querySelectorAll(".threshold-preset-card").forEach(el => { el.disabled = !editable; });
}
async function saveThresholds(quiet) {
  if (!isAdmin()) { toast(I18N.t("toast.admin_only", "仅管理员可操作"), "err"); return false; }
  let ok = false;
  await withLoading("saveThresholdsBtn", async () => {
    try {
      const c = await fetch(`${API}/config`).then(r => r.json()); // 全量配置（密钥已脱敏，回存时后端按原值保留）
      c.thresholds = collectThresholdsFromForm();
      const r = await fetch(`${API}/config`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(c) });
      if (r.ok) {
        ok = true;
        if (!quiet) toast(I18N.t("settings.thresholds_saved", "告警阈值已保存，即时生效"), "ok");
        syncActiveThresholdPreset();
      } else toast(I18N.t("toast.save_failed"), "err");
    } catch (e) { toast(I18N.t("toast.save_failed2") + e, "err"); }
  });
  return ok;
}

// Tab switching for notification channels
function switchNotifyTab(tabId) {
  document.querySelectorAll("#notifyTabs .tab").forEach(btn => btn.classList.toggle("active", btn.dataset.tab === tabId));
  document.querySelectorAll("#settingsMask .tab-panel").forEach(p => p.classList.toggle("active", p.id === tabId));
  if (tabId === "tab-sms") updateSmsProviderFields();
  if (tabId === "tab-voicecall") updateVoiceProviderFields();
}

// Show only the fields required by the selected SMS / voice cloud provider.
function updateSmsProviderFields() {
  const p = ($("smsProvider")?.value || "aliyun").trim();
  document.querySelectorAll("#tab-sms [data-sms-providers]").forEach(el => {
    const list = (el.getAttribute("data-sms-providers") || "").split(",").map(s => s.trim()).filter(Boolean);
    el.style.display = list.includes(p) ? "" : "none";
  });
  const hint = $("smsProviderHint");
  const akL = $("smsAccessKeyLabel");
  const skL = $("smsSecretKeyLabel");
  const appL = $("smsAppIdLabel");
  const ak = $("smsAccessKey");
  const app = $("smsAppId");
  if (p === "huawei") {
    if (hint) hint.textContent = I18N.t("settings.sms_hint_huawei", "华为云短信：需填写 AppKey/AppSecret、project_id、通道号 Sender、签名与模板。");
    if (akL) akL.textContent = I18N.t("settings.sms_ak_huawei", "AppKey");
    if (skL) skL.textContent = I18N.t("settings.sms_sk_huawei", "AppSecret");
    if (appL) appL.textContent = I18N.t("settings.sms_appid_huawei", "Project ID");
    if (ak) ak.placeholder = "华为云 AppKey";
    if (app) app.placeholder = "cn-north-4 项目 Project ID";
  } else if (p === "tencent") {
    if (hint) hint.textContent = I18N.t("settings.sms_hint_tencent", "腾讯云短信：需填写 SecretId/SecretKey、SmsSdkAppId、地域、签名与模板。");
    if (akL) akL.textContent = I18N.t("settings.sms_ak_tencent", "SecretId");
    if (skL) skL.textContent = I18N.t("settings.sms_sk_tencent", "SecretKey");
    if (appL) appL.textContent = I18N.t("settings.sms_appid_tencent", "SmsSdkAppId");
    if (ak) ak.placeholder = "AKIDxxxx";
    if (app) app.placeholder = "SmsSdkAppId";
  } else {
    if (hint) hint.textContent = I18N.t("settings.sms_hint_aliyun", "阿里云短信：需填写 AccessKey、签名 SignName 与模板 TemplateCode。");
    if (akL) akL.textContent = I18N.t("settings.sms_access_key", "AccessKey");
    if (skL) skL.textContent = I18N.t("settings.sms_secret_key", "SecretKey");
    if (ak) ak.placeholder = "LTAI5t...";
  }
}

function updateVoiceProviderFields() {
  const p = ($("voiceCallProvider")?.value || "aliyun").trim();
  document.querySelectorAll("#tab-voicecall [data-voice-providers]").forEach(el => {
    const list = (el.getAttribute("data-voice-providers") || "").split(",").map(s => s.trim()).filter(Boolean);
    el.style.display = list.includes(p) ? "" : "none";
  });
  const hint = $("voiceProviderHint");
  const akL = $("voiceAccessKeyLabel");
  const skL = $("voiceSecretKeyLabel");
  const appL = $("voiceAppIdLabel");
  const ak = $("voiceCallAccessKey");
  const app = $("voiceCallAppId");
  if (p === "huawei") {
    if (hint) hint.textContent = I18N.t("settings.voice_hint_huawei", "华为云语音：需填写 AppKey/AppSecret、project_id、主叫号码与 TTS 模板。");
    if (akL) akL.textContent = I18N.t("settings.sms_ak_huawei", "AppKey");
    if (skL) skL.textContent = I18N.t("settings.sms_sk_huawei", "AppSecret");
    if (appL) appL.textContent = I18N.t("settings.sms_appid_huawei", "Project ID");
    if (ak) ak.placeholder = "华为云 AppKey";
    if (app) app.placeholder = "cn-north-4 项目 Project ID";
  } else if (p === "tencent") {
    if (hint) hint.textContent = I18N.t("settings.voice_hint_tencent", "腾讯云语音：需填写 SecretId/SecretKey、VoiceSdkAppId、地域与 TTS 模板。");
    if (akL) akL.textContent = I18N.t("settings.sms_ak_tencent", "SecretId");
    if (skL) skL.textContent = I18N.t("settings.sms_sk_tencent", "SecretKey");
    if (appL) appL.textContent = I18N.t("settings.voice_appid_tencent", "VoiceSdkAppId");
    if (ak) ak.placeholder = "AKIDxxxx";
    if (app) app.placeholder = "VoiceSdkAppId";
  } else {
    if (hint) hint.textContent = I18N.t("settings.voice_hint_aliyun", "阿里云语音通知：需填写 AccessKey、被叫号码与 TTS 模板编码。");
    if (akL) akL.textContent = I18N.t("settings.voice_access_key", "AccessKey");
    if (skL) skL.textContent = I18N.t("settings.voice_secret_key", "SecretKey");
    if (ak) ak.placeholder = "LTAI5t...";
  }
}

function collectSettings() {
  return {
    alerts_enabled: $("alertsEnabled").checked,
    feishu: { enabled: $("feishuEnabled").checked, webhook: $("feishuWebhook").value.trim() },
    dingtalk: { enabled: $("dingEnabled").checked, webhook: $("dingWebhook").value.trim(), secret: $("dingSecret").value.trim() },
    custom_webhook: {
      enabled: $("customWebhookEnabled").checked,
      url: $("customWebhookURL").value.trim(),
      method: $("customWebhookMethod").value,
      content_type: $("customWebhookContentType").value.trim(),
      headers: $("customWebhookHeaders").value.trim(),
      body_template: $("customWebhookBodyTemplate").value.trim()
    },
    smtp: {
      smtp_enabled: $("smtpEnabled").checked,
      smtp_host: $("smtpHost").value.trim(),
      smtp_port: parseInt($("smtpPort").value) || 0,
      smtp_username: $("smtpUsername").value.trim(),
      smtp_password: $("smtpPassword").value,
      smtp_from_name: $("smtpFromName").value.trim(),
      smtp_use_tls: $("smtpTLS").checked
    },
    sms: {
      enabled: $("smsEnabled").checked,
      provider: $("smsProvider").value,
      access_key: $("smsAccessKey").value.trim(),
      secret_key: $("smsSecretKey").value,
      sign_name: $("smsSignName").value.trim(),
      template_code: $("smsTemplateCode").value.trim(),
      template_param: $("smsTemplateParam").value.trim(),
      app_id: $("smsAppId").value.trim(),
      sender: $("smsSender").value.trim(),
      region: $("smsRegion").value.trim(),
      phones: ($("smsPhones").value || "").split(",").map(s => s.trim()).filter(Boolean)
    },
    voice_call: {
      enabled: $("voiceCallEnabled").checked,
      provider: $("voiceCallProvider").value,
      access_key: $("voiceCallAccessKey").value.trim(),
      secret_key: $("voiceCallSecretKey").value,
      called_numbers: ($("voiceCallCalledNumbers").value || "").split(",").map(s => s.trim()).filter(Boolean),
      tts_code: $("voiceCallTtsCode").value.trim(),
      tts_param: $("voiceCallTtsParam").value.trim(),
      app_id: $("voiceCallAppId").value.trim(),
      display_nbr: $("voiceCallDisplayNbr").value.trim(),
      region: $("voiceCallRegion").value.trim()
    }
    // 注意：告警阈值由独立的「告警阈值」Tab（saveThresholds）管理，此处不再序列化 thresholds，
    // 否则保存告警通知设置会用一份不完整的阈值覆盖掉 check_*/GPU 温度·显存/连接数 等字段（被后端
    // 零值回填成默认值 → 静默丢失用户自定义阈值）。saveSettings 改为「拉全量→仅覆盖通知字段→回存」。
  };
}
async function saveSettings() {
  if (!isAdmin()) { toast(I18N.t("toast.admin_only", "仅管理员可操作"), "err"); return; }
  await withLoading("saveBtn", async () => {
    try {
      // 拉全量配置，仅覆盖告警通知字段后回存，避免清空 thresholds 等由其它 Tab 管理的设置。
      const full = await fetch(`${API}/config`).then(r => r.json());
      Object.assign(full, collectSettings());
      const r = await fetch(`${API}/config`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(full) });
      if (r.ok) { toast(I18N.t("toast.config_saved"), "ok"); $("settingsMask").classList.remove("show"); } else { toast(I18N.t("toast.save_failed"), "err"); }
    } catch (e) { toast(I18N.t("toast.save_failed2") + e, "err"); }
  });
}
async function testSettings() {
  if (!isAdmin()) { toast(I18N.t("toast.admin_only", "仅管理员可操作"), "err"); return; }
  await withLoading("testBtn", async () => {
    try {
      const r = await fetch(`${API}/config/test`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(collectSettings()) });
      const j = await r.json();
      if (j.ok) toast(I18N.t("toast.test_sent"), "ok");
      else toast(I18N.t("toast.test_failed2") + (j.errors || []).join("; "), "err");
    } catch (e) { toast(I18N.t("toast.test_failed2") + e, "err"); }
  });
}

/* ---------- 安装 Agent ---------- */
let INSTALL = { server_url: "", token: "" };
let CUR_OS = "linux";
let RELAY_MODE = false;
let MULTI_SERVER_MODE = false;
let TOKEN_REVEALED = true; // 默认明文：脱敏展示会导致用户复制到无法安装的命令
// Windows Server 2012 R2 / 2016 default to TLS 1.0. The install.ps1 template enables
// TLS 1.2 internally, but that runs too late: the outer `irm` that DOWNLOADS the
// script fails first against a TLS1.2-only HTTPS server ("未能创建 SSL/TLS 安全通道").
// So the one-liner must enable TLS 1.2 itself, before irm. Numeric 3072 = Tls12,
// -bor keeps existing protocols; using the number avoids an enum-undefined error on
// older .NET where the [Net.SecurityProtocolType]::Tls12 name isn't defined.
const PS_TLS12 = "[Net.ServicePointManager]::SecurityProtocol=[Net.ServicePointManager]::SecurityProtocol -bor 3072; ";
function maskToken(t) {
  if (!t) return "";
  if (TOKEN_REVEALED) return t;
  if (t.length <= 8) return "••••••••";
  return t.slice(0, 4) + "••••••••" + t.slice(-4);
}
function updateTokenDisplay() {
  const el = $("installToken"); if (!el) return;
  el.value = maskToken(INSTALL.token || "");
  el.dataset.revealed = TOKEN_REVEALED ? "1" : "0";
}
function unixToDatetimeLocal(sec) {
  const n = parseInt(sec, 10) || 0;
  if (n <= 0) return "";
  const d = new Date(n * 1000);
  if (isNaN(d.getTime())) return "";
  const pad = (x) => String(x).padStart(2, "0");
  return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) +
    "T" + pad(d.getHours()) + ":" + pad(d.getMinutes());
}
function datetimeLocalToUnix(val) {
  const s = String(val || "").trim();
  if (!s) return 0;
  const t = Date.parse(s);
  if (isNaN(t)) return 0;
  return Math.floor(t / 1000);
}
function syncInstallTokenPolicyBadge(info) {
  const badge = $("installTokenPolicyBadge");
  if (!badge) return;
  if (info && info.revoked) {
    badge.textContent = I18N.t("install.token_revoked_badge", "已吊销");
    badge.classList.add("off");
    return;
  }
  badge.classList.remove("off");
  const uses = (info && info.max_uses > 0)
    ? I18N.t("install.token_uses_limited", "限 {n} 次").replace("{n}", String(info.max_uses))
    : I18N.t("install.token_uses_unlimited", "不限次数");
  const exp = (info && info.expires_at > 0)
    ? I18N.t("install.token_expires_at", "过期 {t}").replace("{t}", new Date(info.expires_at * 1000).toLocaleString())
    : I18N.t("install.token_never_expires", "永不过期");
  badge.textContent = uses + " · " + exp;
}
function collapseInstallTokenPolicy() {
  const body = $("installTokenPolicyBody"), caret = $("installTokenPolicyCaret");
  const head = $("installTokenPolicyToggle");
  if (body) body.style.display = "none";
  if (caret) caret.textContent = "▸";
  if (head) head.setAttribute("aria-expanded", "false");
}
function renderInstallTokenMeta(info) {
  const el = $("installTokenMeta");
  if (!el || !info) return;
  const parts = [];
  if (info.revoked) parts.push(I18N.t("install.token_status_revoked", "状态：已吊销"));
  else parts.push(I18N.t("install.token_status_valid", "状态：有效"));
  parts.push(I18N.t("install.token_used", "已用 {n}").replace("{n}", String(info.use_count || 0)) +
    (info.max_uses > 0 ? ` / ${info.max_uses}` : I18N.t("install.token_used_unlimited", " 次（不限）")));
  if (info.expires_at > 0) parts.push(I18N.t("install.token_expires_prefix", "过期：") + new Date(info.expires_at * 1000).toLocaleString());
  if (info.prev_valid_until > 0) parts.push(I18N.t("install.token_grace", "旧 Token 宽限至 ") + new Date(info.prev_valid_until * 1000).toLocaleString());
  el.textContent = parts.join(" · ");
  if ($("installTokenMaxUses")) $("installTokenMaxUses").value = info.max_uses || 0;
  if ($("installTokenExpiresAt")) $("installTokenExpiresAt").value = unixToDatetimeLocal(info.expires_at || 0);
  syncInstallTokenPolicyBadge(info);
}
async function populateInstallCategoryList() {
  const sel = $("installFolderId");
  if (!sel) return;
  const prev = sel.value;
  try {
    const data = await fetch(`${API}/host-folders`).then((r) => r.json());
    const flat = typeof flattenHostFolders === "function"
      ? flattenHostFolders(data.folders || [])
      : [];
    sel.innerHTML = "";
    const empty = document.createElement("option");
    empty.value = "";
    empty.textContent = I18N.t("install.category_placeholder", "不分组（可选任意节点）");
    sel.appendChild(empty);
    for (const n of flat) {
      const opt = document.createElement("option");
      opt.value = n.id;
      opt.textContent = n.path;
      opt.dataset.name = n.name || "";
      sel.appendChild(opt);
    }
    if (prev && [...sel.options].some((o) => o.value === prev)) sel.value = prev;
  } catch (_) {
    /* leave select with empty option */
  }
  syncInstallNewFolderBtn();
}
function syncInstallNewFolderBtn() {
  const btn = $("installNewFolderBtn");
  const sel = $("installFolderId");
  if (!btn || !sel) return;
  btn.textContent = sel.value
    ? I18N.t("install.new_child_folder_btn", "新建子分组")
    : I18N.t("install.new_root_folder_btn", "新建一级分组");
}
function installFolderQueryParts() {
  const sel = $("installFolderId");
  const fid = (sel && sel.value) || "";
  const opt = sel && sel.selectedOptions && sel.selectedOptions[0];
  const cat = (opt && (opt.dataset.name || "").trim()) || "";
  let q = "";
  if (fid) q += "&folder_id=" + encodeURIComponent(fid);
  if (cat) q += "&category=" + encodeURIComponent(cat);
  return q;
}
async function createInstallChildFolder() {
  const sel = $("installFolderId");
  const parent = (sel && sel.value) || "";
  const promptMsg = parent
    ? I18N.t("install.new_child_folder_prompt", "在当前所选节点下新建子分组名称")
    : I18N.t("install.new_root_folder_prompt", "新建一级分组名称");
  const name = (prompt(promptMsg) || "").trim();
  if (!name) return;
  try {
    const r = await fetch(`${API}/host-folders`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({ name, parent_id: parent }),
    });
    const data = await r.json();
    if (!r.ok) throw new Error(data.error || r.statusText);
    await populateInstallCategoryList();
    if (data.folder && data.folder.id && sel) {
      sel.value = data.folder.id;
      syncInstallNewFolderBtn();
    }
    renderInstallCmd();
    toast(I18N.t("install.folder_created", "分组已创建"), "ok");
  } catch (e) {
    toast(I18N.t("install.folder_create_failed", "创建分组失败：") + e, "err");
  }
}
async function openInstall() {
  try {
    INSTALL = await fetch(`${API}/install/info`).then(r => r.json());
    TOKEN_REVEALED = true;
    updateTokenDisplay();
    renderInstallTokenMeta(INSTALL);
    RELAY_MODE = false;
    MULTI_SERVER_MODE = false;
    const normalRadio = document.querySelector('input[name="installMode"][value="normal"]');
    if (normalRadio) normalRadio.checked = true;
    await populateInstallCategoryList();
    renderInstallCmd();
    $("installMask").classList.add("show");
    collapseInstallTokenPolicy();
    loadAgentAutoUpdatePolicy();
  } catch (e) { toast(I18N.t("toast.read_install_failed") + e, "err"); }
}

function syncAgentAutoUpdateBadge() {
  const badge = $("agentAutoUpdateBadge");
  const on = !!($("agentAutoUpdate") && $("agentAutoUpdate").checked);
  if (!badge) return;
  badge.textContent = on
    ? I18N.t("agent_update.badge_on", "已开启")
    : I18N.t("agent_update.badge_off", "已关闭");
  badge.classList.toggle("off", !on);
}
async function loadAgentAutoUpdatePolicy() {
  try {
    const p = await fetch(`${API}/agents/auto-update-policy`, { credentials: "same-origin" }).then(r => r.json());
    // Default-on: if the API omits the field, keep the checkbox checked.
    if ($("agentAutoUpdate")) {
      $("agentAutoUpdate").checked = (typeof p.agent_auto_update === "boolean")
        ? !!p.agent_auto_update
        : true;
    }
    if ($("agentAutoUpdateWindow")) $("agentAutoUpdateWindow").value = p.agent_auto_update_window || "";
    if ($("agentAutoUpdateExemptCat")) {
      $("agentAutoUpdateExemptCat").value = Array.isArray(p.agent_auto_update_exempt_categories)
        ? p.agent_auto_update_exempt_categories.join(",") : "";
    }
    if ($("agentAutoUpdateExemptHosts")) {
      $("agentAutoUpdateExemptHosts").value = Array.isArray(p.agent_auto_update_exempt_hosts)
        ? p.agent_auto_update_exempt_hosts.join(",") : "";
    }
    // Keep the advanced policy panel collapsed by default on each open.
    const body = $("agentAutoUpdateBody"), caret = $("agentAutoUpdateCaret");
    const head = $("agentAutoUpdateToggle");
    if (body) body.style.display = "none";
    if (caret) caret.textContent = "▸";
    if (head) head.setAttribute("aria-expanded", "false");
    syncAgentAutoUpdateBadge();
    loadAgentAutoUpdateStatus();
  } catch (e) {
    if ($("agentAutoUpdate")) $("agentAutoUpdate").checked = true;
    syncAgentAutoUpdateBadge();
  }
}

async function saveAgentAutoUpdatePolicy() {
  if (!isAdmin()) { toast(I18N.t("toast.admin_only", "仅管理员可操作"), "err"); return; }
  const splitCSV = (s) => String(s || "").split(",").map(x => x.trim()).filter(Boolean);
  const body = {
    agent_auto_update: !!($("agentAutoUpdate") && $("agentAutoUpdate").checked),
    agent_auto_update_window: (($("agentAutoUpdateWindow") || {}).value || "").trim(),
    agent_auto_update_exempt_categories: splitCSV(($("agentAutoUpdateExemptCat") || {}).value),
    agent_auto_update_exempt_hosts: splitCSV(($("agentAutoUpdateExemptHosts") || {}).value)
  };
  try {
    const r = await fetch(`${API}/agents/auto-update-policy`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) {
      toast((j && j.error) || I18N.t("toast.save_failed", "保存失败"), "err");
      return;
    }
    toast(I18N.t("agent_update.policy_saved", "自动更新策略已保存"), "ok");
    syncAgentAutoUpdateBadge();
    loadAgentAutoUpdateStatus();
  } catch (e) {
    toast(I18N.t("toast.save_failed2", "保存失败:") + e, "err");
  }
}

// 自动升级运行情况：目标版本 / 周期扫描心跳 / 每主机跳过原因（为什么没升级）。
// 与 v2 版 SettingsView 的「自动升级运行情况」面板同源接口，保证两版口径一致。
async function loadAgentAutoUpdateStatus() {
  const meta = $("agentAutoStatusMeta"), skips = $("agentAutoStatusSkips");
  if (!meta && !skips) return;
  let st;
  try {
    const r = await fetch(`${API}/agents/auto-update-status`, { credentials: "same-origin" });
    if (!r.ok) throw new Error("HTTP " + r.status);
    st = await r.json();
  } catch (e) {
    if (meta) meta.innerHTML = `<span class="muted">运行情况暂不可用（${esc(String(e.message || e))}）</span>`;
    if (skips) skips.innerHTML = "";
    return;
  }
  const parts = [];
  parts.push(`<span>目标版本 <b class="mono">${esc(st.target_version || "-")}</b></span>`);
  if (st.comparable === false) {
    parts.push(`<span class="agent-auto-status-warn">服务端版本号不可比较（构建时未注入 vX.Y.Z），自动升级不会触发</span>`);
  }
  parts.push(`<span>上次周期扫描 <b>${st.last_scan_at ? esc(fmtDateTime(st.last_scan_at)) : "尚未扫描"}</b></span>`);
  if (meta) meta.innerHTML = parts.join("");
  // 必须在「跳过列表为空」的提前 return 之前调用：没有跳过记录恰恰说明任务**已经入队**，
  // 也就是最需要看任务结果的时候。
  loadAgentAutoUpdateJobs();
  if (!skips) return;
  const list = Array.isArray(st.skips) ? st.skips : [];
  if (!list.length) {
    skips.innerHTML = `<div class="hint" style="margin-top:8px">暂无跳过记录（所有在线主机均已满足自动升级条件或已完成升级）</div>`;
    return;
  }
  const rows = list.slice(0, 50).map(s => {
    const name = s.hostname || (s.host_id ? s.host_id.slice(0, 8) : "-");
    const detail = s.detail ? ` <span class="muted">${esc(s.detail)}</span>` : "";
    return `<tr><td class="mono">${esc(name)}</td><td>${esc(s.reason || "-")}${detail}</td><td class="mono">${s.at ? esc(fmtDateTime(s.at)) : "-"}</td></tr>`;
  }).join("");
  skips.innerHTML = `<div class="agent-auto-skips-title">最近未升级的主机与原因</div>
    <div style="overflow:auto;max-height:220px"><table class="agent-auto-skips-tbl"><thead><tr><th>主机</th><th>原因</th><th>时间</th></tr></thead><tbody>${rows}</tbody></table></div>`;
}
// 最近的自动升级任务：把「后台跑了、失败了、没人看见」变成看得见。
// 每台主机带 method（module / script / script-rescue）与 message —— 后者在 Windows
// 换版失败时会捎回主机本地助手日志的尾巴（服务端 windowsUpdateEvidence 取回）。
//
// message 不再截断：服务端已经按 1200 字封顶，而 Windows 失败信息里真正解释原因的
// 那一段（" | host evidence: …"、「上一轮升级助手留下的记录」）全在**结尾**——前端
// 再切一刀，剩下的就只有一句什么也没说的开场白。
async function loadAgentAutoUpdateJobs() {
  const box = $("agentAutoStatusJobs");
  if (!box) return;
  let jobs;
  try {
    jobs = await fetch(`${API}/agents/update/jobs?limit=5`, { credentials: "same-origin" }).then(r => r.json());
  } catch (e) {
    box.innerHTML = `<div class="hint" style="margin-top:8px">最近任务不可用（${esc(String(e.message || e))}）</div>`;
    return;
  }
  const list = Array.isArray(jobs) ? jobs : [];
  if (!list.length) {
    box.innerHTML = `<div class="hint" style="margin-top:8px">最近没有升级任务（自动升级未触发，或所有主机已是目标版本）</div>`;
    return;
  }
  const rows = [];
  list.slice(0, 5).forEach(j => {
    (j.hosts || []).forEach(h => {
      const bad = h.status === "failed" || h.status === "pending_verify";
      rows.push(`<tr class="${bad ? "agent-job-row-bad" : ""}">
        <td class="mono">${esc(h.hostname || (h.host_id || "").slice(0, 8))}</td>
        <td>${esc(h.status || "-")}</td>
        <td>${esc(h.method || "-")}</td>
        <td class="mono">${esc(h.from_version || "-")}→${esc(j.target_version || "-")}</td>
        <td style="max-width:520px;word-break:break-all;white-space:pre-wrap">${esc(String(h.message || ""))}</td>
        <td class="mono">${j.created_at ? esc(fmtDateTime(j.created_at)) : "-"}</td>
      </tr>`);
    });
  });
  if (!rows.length) {
    box.innerHTML = `<div class="hint" style="margin-top:8px">最近任务里没有主机记录</div>`;
    return;
  }
  box.innerHTML = `<div class="agent-auto-skips-title">最近的升级任务（每台主机的真实结果）</div>
    <div style="overflow:auto;max-height:280px"><table class="agent-auto-skips-tbl">
    <thead><tr><th>主机</th><th>状态</th><th>方式</th><th>版本</th><th>消息</th><th>时间</th></tr></thead>
    <tbody>${rows.join("")}</tbody></table></div>`;
}

function normalizeInstallServerURL(u) {
  return String(u || "").trim().replace(/\/+$/, "").toLowerCase();
}
function normalizeExtraServerURL(raw) {
  const s = String(raw || "").trim();
  if (!s) return "";
  if (/^https?:\/\//i.test(s)) return s.replace(/\/+$/, "");
  if (/[/?#@\s]/.test(s)) return s;
  if (s.startsWith("[")) {
    const m = s.match(/^(\[[0-9a-fA-F:.]+\])(?::(\d+))?$/i);
    if (!m) return s;
    return m[2] ? ("http://" + m[1] + ":" + m[2]) : ("http://" + m[1]);
  }
  if (/^[0-9a-fA-F:]+$/i.test(s) && (s.includes("::") || ((s.match(/:/g) || []).length >= 2))) {
    return "http://[" + s + "]";
  }
  if (/^[\w.-]+(?::\d+)?$/.test(s)) return ("http://" + s).replace(/\/+$/, "");
  return s;
}
function parseListenPort(listen) {
  const s = String(listen || "").trim();
  const m = s.match(/:(\d+)\s*$/) || s.match(/^(\d+)$/);
  const p = m ? parseInt(m[1], 10) : 0;
  if (!p || p < 1 || p > 65535) return 0;
  return p;
}
function parseMultiServerExtras() {
  const text = ($("multiServerList") || {}).value || "";
  const lines = text.split("\n").map(l => l.trim()).filter(l => l);
  const servers = [];
  for (const line of lines) {
    const parts = line.split(/\s+/);
    const server = normalizeExtraServerURL(parts[0]);
    const token = parts.slice(1).join(" ") || "";
    if (server) servers.push({ server, token });
  }
  return servers;
}
/** Current panel + extras; current panel is always first. */
function buildMultiServerTargets() {
  const primary = INSTALL.server_url || location.origin;
  const token = INSTALL.token || "";
  const out = [{ server: primary, token }];
  const seen = new Set([normalizeInstallServerURL(primary)]);
  const maxTargets = 8;
  for (const e of parseMultiServerExtras()) {
    if (out.length >= maxTargets) break;
    const key = normalizeInstallServerURL(e.server);
    if (!key || seen.has(key) || !validHTTPURL(e.server)) continue;
    seen.add(key);
    out.push({ server: e.server, token: e.token || "" });
  }
  return out;
}
function formatRelayBase(ip, port) {
  const host = String(ip || "").trim();
  const p = typeof port === "number" ? port : parseListenPort(port);
  if (!host || !p || !validRelayHost(host)) return "";
  if (host.startsWith("[")) return `http://${host}:${p}`;
  if (host.includes(":")) return `http://[${host}]:${p}`;
  return `http://${host}:${p}`;
}
function updateInstallModeHint() {
  const el = $("installModeHint");
  if (!el) return;
  const exclusive = I18N.t("install.mode_exclusive_hint", "中继与多服务端互斥，请勿在同一 Agent 上同时配置。");
  if (RELAY_MODE) {
    el.textContent = I18N.t("install.relay_hint", "仅一台机器能联网时使用") + " · " + exclusive;
  } else if (MULTI_SERVER_MODE) {
    el.textContent = I18N.t("install.multi_server_hint", "单 Agent 同时向多个服务端推送") + " · " + exclusive;
  } else {
    el.textContent = I18N.t("install.mode_normal_desc", "直连本面板安装");
  }
}
function validHTTPURL(value) {
  try {
    const parsed = new URL(String(value || "").trim());
    return (parsed.protocol === "http:" || parsed.protocol === "https:") && !!parsed.host && !parsed.username && !parsed.password;
  } catch (_) {
    return false;
  }
}
function validRelayHost(value) {
  const host = String(value || "").trim();
  if (!host || /\s|[/?#@]/.test(host)) return false;
  if (host.startsWith("[")) return /^\[[0-9a-fA-F:.]+\]$/.test(host);
  if (host.includes(":")) {
    const colons = (host.match(/:/g) || []).length;
    return /^[0-9a-fA-F:]+$/i.test(host) && (host.includes("::") || colons >= 2);
  }
  return /^[A-Za-z0-9._-]+$/.test(host);
}
function maskInstallCmd(cmd) {
  const t = (INSTALL && INSTALL.token) || "";
  if (!t || TOKEN_REVEALED) return cmd;
  return String(cmd || "").split(t).join("••••••••");
}
function renderInstallCmd() {
  // Multi-server section visibility
  const msSection = $("multiServerSection");
  if (msSection) msSection.style.display = (MULTI_SERVER_MODE && !RELAY_MODE) ? "" : "none";
  const auditSection = $("networkAuditInstallSection");
  if (auditSection) auditSection.style.display = !RELAY_MODE ? "" : "none";
  updateInstallModeHint();
  // Relay mode: show gateway + internal commands, hide normal install
  if (RELAY_MODE) {
    $("normalInstallSection").style.display = "none";
    $("relaySection").style.display = "";
    renderRelayCmd();
    return;
  }
  $("normalInstallSection").style.display = "";
  $("relaySection").style.display = "none";
  const server = INSTALL.server_url || location.origin;
  const token = INSTALL.token || "";
  let q = "token=" + encodeURIComponent(token) + installFolderQueryParts();
  // 日志采集（可选）：把用户填写的路径（换行/逗号分隔）拼进安装命令，服务端写入 config.json 的 log_paths
  const lp = (($("installLogPaths") && $("installLogPaths").value) || "").trim();
  if (lp) q += "&log_paths=" + encodeURIComponent(lp);
  // Cross-platform network visibility. Linux auto-selects AF_PACKET;
  // Windows/macOS auto-select TShark over Npcap/libpcap/BPF.
  if (!RELAY_MODE) {
    const contentAudit = !!($("installContentAudit") && $("installContentAudit").checked);
    const sniEnabled = contentAudit || !!($("installSNIEnabled") && $("installSNIEnabled").checked);
    if (sniEnabled) q += "&sni_enabled=1";
    let backend = (($("installCaptureBackend") || {}).value || "auto").trim();
    if (CUR_OS !== "linux" && backend === "native") backend = "auto";
    q += "&capture_backend=" + encodeURIComponent(backend);
    const iface = (($("installSNIInterface") || {}).value || "").trim();
    if (iface) q += "&sni_interface=" + encodeURIComponent(iface);
    if (contentAudit) {
      q += "&content_audit=1";
      const ports = (($("installContentAuditPorts") || {}).value || "").trim();
      if (ports) q += "&content_audit_ports=" + encodeURIComponent(ports);
      const maxBody = parseInt((($("installContentAuditMaxBody") || {}).value || "4096"), 10) || 4096;
      q += "&content_audit_max_body=" + encodeURIComponent(String(maxBody));
      const bodyMode = (($("installContentAuditBodyMode") || {}).value || "redacted").trim();
      q += "&content_audit_body_mode=" + encodeURIComponent(bodyMode);
      const maxEvents = parseInt((($("installContentAuditMaxEvents") || {}).value || "2000"), 10) || 2000;
      q += "&content_audit_max_events_per_min=" + encodeURIComponent(String(maxEvents));
      const hosts = (($("installContentAuditHosts") || {}).value || "").trim();
      if (hosts) q += "&content_audit_include_hosts=" + encodeURIComponent(hosts);
      const excluded = (($("installContentAuditExcludePaths") || {}).value || "").trim();
      if (excluded) q += "&content_audit_exclude_paths=" + encodeURIComponent(excluded);
    }
  }
  // Multi-server: always include current panel + extras as servers_json.
  let cmd, label, hint;
  const multiWarn = $("multiServerWarn");
  const multiUrlWarn = $("multiServerUrlWarn");
  let multiReady = true;
  let multiValid = true;
  if (MULTI_SERVER_MODE) {
    const targets = buildMultiServerTargets();
    multiReady = targets.length >= 2;
    const extras = parseMultiServerExtras();
    multiValid = extras.every((t) => validHTTPURL(t.server));
    if (multiWarn) multiWarn.style.display = multiReady ? "none" : "";
    if (multiUrlWarn) multiUrlWarn.style.display = multiReady && !multiValid ? "" : "none";
    q += "&servers_json=" + encodeURIComponent(JSON.stringify(targets));
  } else {
    if (multiWarn) multiWarn.style.display = "none";
    if (multiUrlWarn) multiUrlWarn.style.display = "none";
  }
  const multiHint = MULTI_SERVER_MODE
    ? (multiReady
      ? (multiValid
        ? I18N.t("install.multi_desc", "一台 Agent 同时向多个服务端推送")
        : I18N.t("install.invalid_extra_server", "额外服务端 URL 无效，请使用 http(s)://host[:port]"))
      : I18N.t("install.multi_need_two"))
    : "";
  if (CUR_OS === "windows") {
    cmd = `${PS_TLS12}irm "${server}/install.ps1?${q}" | iex`;
    label = MULTI_SERVER_MODE
      ? (I18N.t("install.multi_server") + " · " + I18N.t("install.powershell_cmd"))
      : I18N.t("install.powershell_cmd");
    hint = multiHint || I18N.t("install.windows_desc");
  } else if (CUR_OS === "macos") {
    cmd = `curl -fsSL "${server}/install.sh?${q}" | sh`;
    label = MULTI_SERVER_MODE
      ? (I18N.t("install.multi_server") + " · " + I18N.t("install.terminal_one_line"))
      : I18N.t("install.terminal_one_line");
    hint = multiHint || I18N.t("install.linux_detail");
  } else {
    // Demo-safe default: non-root install under ~/.aiops-agent (no sudo).
    cmd = `curl -fsSL "${server}/install.sh?${q}" | sh`;
    label = MULTI_SERVER_MODE
      ? (I18N.t("install.multi_server") + " · " + I18N.t("install.linux_cmd"))
      : I18N.t("install.linux_cmd");
    hint = multiHint || I18N.t("install.linux_desc");
  }
  $("installCmd").textContent = maskInstallCmd(cmd);
  $("cmdLabel").textContent = label;
  $("cmdHint").textContent = hint;
  const copyBtn = $("copyCmdBtn");
  if (copyBtn) {
    copyBtn.disabled = MULTI_SERVER_MODE && (!multiReady || !multiValid);
    copyBtn.dataset.rawCmd = cmd;
  }
  $("uninstallCmd").textContent = (CUR_OS === "windows")
    ? `${PS_TLS12}irm "${server}/uninstall.ps1" | iex`
    : `curl -fsSL "${server}/uninstall.sh" | sh`;
}
function renderRelayCmd() {
  const server = INSTALL.server_url || location.origin;
  const token = INSTALL.token || "";
  let q = "token=" + encodeURIComponent(token) + installFolderQueryParts();
  // Internal agents (via relay) still accept log_paths; audit stays off in relay mode.
  const lp = (($("installLogPaths") && $("installLogPaths").value) || "").trim();
  if (lp) q += "&log_paths=" + encodeURIComponent(lp);
  const gwIP = (($("relayGatewayIP") || {}).value || "").trim();
  const port = parseListenPort((($("relayListenPort") || {}).value || "8529"));
  const relayReady = validRelayHost(gwIP) && port > 0;
  const relay = relayReady ? formatRelayBase(gwIP, port) : "";
  const listenEnv = port ? `RELAY_LISTEN=:${port}` : "";
  let gatewayCmd = "", internalCmd = "";
  if (port) {
    if (CUR_OS === "windows") {
      gatewayCmd = `${PS_TLS12}$env:RELAY_LISTEN=':${port}'; irm "${server}/install-relay.ps1" | iex`;
      internalCmd = relayReady ? `${PS_TLS12}irm "${relay}/install.ps1?${q}" | iex` : "";
    } else if (CUR_OS === "macos") {
      gatewayCmd = `curl -fsSL "${server}/install-relay.sh" | env ${listenEnv} sh`;
      internalCmd = relayReady ? `curl -fsSL "${relay}/install.sh?${q}" | sh` : "";
    } else {
      // sudo -E keeps AIOPS_RELAY_SECRET; env sets listen port for the install script.
      gatewayCmd = `curl -fsSL "${server}/install-relay.sh" | sudo -E env ${listenEnv} sh`;
      internalCmd = relayReady ? `curl -fsSL "${relay}/install.sh?${q}" | sh` : "";
    }
  }
  $("relayGatewayCmd").textContent = gatewayCmd || I18N.t("install.invalid_port", "请填写有效的监听端口");
  $("relayInternalCmd").textContent = maskInstallCmd(internalCmd) || I18N.t("install.fill_gateway_first", "请先填写有效的网关内网 IP（灰色提示不会自动填入）");
  const copyBtn = $("copyCmdBtn");
  if (copyBtn) copyBtn.disabled = !relayReady;
  const copyGw = $("copyRelayGatewayBtn");
  if (copyGw) {
    copyGw.disabled = !gatewayCmd;
    copyGw.dataset.rawCmd = gatewayCmd;
  }
  const copyIn = $("copyRelayInternalBtn");
  if (copyIn) {
    copyIn.disabled = !relayReady || !internalCmd;
    copyIn.dataset.rawCmd = internalCmd;
  }
  $("uninstallCmd").textContent = !relayReady
    ? I18N.t("install.fill_gateway_first", "请先填写有效的网关内网 IP")
    : ((CUR_OS === "windows")
      ? `${PS_TLS12}irm "${relay}/uninstall.ps1" | iex`
      : `curl -fsSL "${relay}/uninstall.sh" | sh`);
}
async function resetToken() {
  const ok = typeof uiConfirm === "function"
    ? await uiConfirm({
        title: I18N.t("install.reset_title", "重置安装 Token"),
        message: I18N.t("install.reset_warning"),
        detail: I18N.t("install.reset_detail", "仅影响新 Agent 注册；已装 Agent 靠机器指纹鉴权，不受影响。"),
        confirmText: I18N.t("install.reset_confirm", "重置 Token"),
        tone: "warn"
      })
    : confirm(I18N.t("install.reset_warning"));
  if (!ok) return;
  try {
    const j = await fetch(`${API}/install/reset-token`, { method: "POST" }).then(r => r.json());
    INSTALL.token = j.token; INSTALL.revoked = false; INSTALL.use_count = 0;
    TOKEN_REVEALED = true; updateTokenDisplay(); renderInstallTokenMeta(INSTALL); renderInstallCmd();
    toast(I18N.t("toast.token_reset"), "ok");
  } catch (e) { toast(I18N.t("toast.reset_failed2") + e, "err"); }
}
async function revokeInstallToken() {
  const ok = typeof uiConfirm === "function"
    ? await uiConfirm({
        title: I18N.t("install.revoke_title", "吊销安装 Token"),
        message: I18N.t("install.revoke_warning", "确定吊销当前安装 Token？新 Agent 将无法用该 Token 注册。"),
        detail: I18N.t("install.revoke_detail", "已注册 Agent 不受影响。可再点重置生成新 Token。"),
        confirmText: I18N.t("install.revoke_confirm", "吊销 Token"),
        tone: "danger"
      })
    : confirm(I18N.t("install.revoke_warning", "确定吊销当前安装 Token？新 Agent 将无法用该 Token 注册；已注册 Agent 不受影响。可再点重置生成新 Token。"));
  if (!ok) return;
  try {
    const r = await fetch(`${API}/install/revoke-token`, { method: "POST" });
    if (!r.ok) { toast(I18N.t("install.revoke_failed", "吊销失败"), "err"); return; }
    INSTALL.revoked = true; renderInstallTokenMeta(INSTALL);
    toast(I18N.t("install.revoke_ok", "安装 Token 已吊销"), "ok");
  } catch (e) { toast(I18N.t("install.revoke_failed", "吊销失败") + ": " + e, "err"); }
}
async function saveInstallTokenPolicy() {
  const body = {
    max_uses: parseInt(($("installTokenMaxUses") || {}).value, 10) || 0,
    expires_at: datetimeLocalToUnix(($("installTokenExpiresAt") || {}).value),
  };
  try {
    const r = await fetch(`${API}/install/token-policy`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
    });
    if (!r.ok) { toast(I18N.t("install.policy_save_failed", "保存策略失败"), "err"); return; }
    INSTALL.max_uses = body.max_uses; INSTALL.expires_at = body.expires_at;
    renderInstallTokenMeta(INSTALL);
    toast(I18N.t("install.policy_saved", "Token 策略已保存"), "ok");
  } catch (e) { toast(I18N.t("install.policy_save_failed", "保存失败") + ": " + e, "err"); }
}

/* ---------- 自定义监控 ---------- */
// 进程类目标形如 hostID/进程名，展示为「进程 @ 主机名」更友好。
function checkTargetDisplay(c) {
  if (c.type === "process") {
    const i = c.target.indexOf("/");
    if (i > 0) {
      const hid = c.target.slice(0, i), pname = c.target.slice(i + 1);
      const meta = HOST_META.find(h => h.id === hid);
      return pname + " @ " + (meta ? meta.hostname || hid.slice(0, 8) : hid.slice(0, 8));
    }
  }
  return c.target;
}
// TCP 目标拆分为 主机 / 端口（末个冒号分隔）
function splitHostPort(t) {
  t = String(t || "");
  const i = t.lastIndexOf(":");
  if (i > 0) return { host: t.slice(0, i), port: t.slice(i + 1) };
  return { host: t, port: "" };
}
// 进程目标 hostID/进程名 拆分，并把 hostID 解析为主机名
function splitProcessTarget(c) {
  const t = String(c.target || "");
  const i = t.indexOf("/");
  if (i > 0) {
    const hid = t.slice(0, i), proc = t.slice(i + 1);
    const meta = HOST_META.find(h => h.id === hid);
    return { proc, hostName: meta ? (meta.hostname || hid.slice(0, 8)) : hid.slice(0, 8) };
  }
  return { proc: t, hostName: "—" };
}
// 详情项：键 + 值 + 值配色
function cdItem(k, v, cls) {
  return `<div class="cd-item"><div class="cd-k">${k}</div><div class="cd-v ${cls || ""}" title="${esc(v)}">${esc(v)}</div></div>`;
}
function renderChecks(checks) {
  LAST_CHECKS = checks;
  const userChecks = checks.filter(c => !c.builtin);
  $("navChecks").textContent = userChecks.filter(c => !c.ok && c.checked_at).length || userChecks.length;
  const grid = $("checksGrid"), empty = $("checksEmpty");
  grid.className = "checks-list" + (CHECK_VIEW === "pill" ? " pill" : "");
  if (!userChecks.length && !checks.length) { grid.innerHTML = ""; empty.style.display = "block"; return; }
  empty.style.display = "none";

  // 应用类型筛选 + 关键字搜索
  let shown = checks;
  if (CHECK_TYPE && CHECK_TYPE !== "all") shown = shown.filter(c => c.type === CHECK_TYPE);
  if (CHECK_SEARCH) {
    shown = shown.filter(c => matchesSearchTokens(
      [c.name, c.target, c.id, c.type].filter(Boolean).join(" "),
      CHECK_SEARCH
    ));
  }

  grid.innerHTML = shown.map(c => {
    const st = !c.enabled ? "unknown" : (c.checked_at ? (c.ok ? "up" : "down") : "unknown");
    const stText = !c.enabled ? I18N.t("ui.disabled_status") : (c.checked_at ? (c.ok ? I18N.t("ui.normal") : I18N.t("ui.abnormal")) : I18N.t("ui.pending"));
    const typeText = c.type === "http" ? "HTTP" : c.type === "tcp" ? "TCP" : c.type === "ping" ? "Ping" : I18N.t("ui.process");
    const builtin = c.builtin ? ' data-builtin="1"' : "";
    const histBtn = `<button class="mini-btn" data-cact="hist" title="${I18N.t('ui.history_chart')}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3v18h18"/><path d="M7 13l3-3 3 2 5-6"/></svg></button>`;
    const actions = `<span class="ch-actions">${histBtn}${c.builtin ? "" : `
          <button class="mini-btn" data-cact="run" title="${I18N.t('ui.check_now')}">▶</button>
          <button class="mini-btn" data-cact="edit" title="${I18N.t('ui.edit')}">✎</button>
          <button class="mini-btn del" data-cact="del" title="${I18N.t('ui.delete')}">✕</button>`}</span>`;
    const builtinTag = c.builtin ? `<span class="type-badge" style="background:var(--ok-soft);color:var(--ok-txt)">${I18N.t("ui.builtin")}</span>` : "";

    // 详情字段：按监控类型给出各自贴合的字段，三类监控信息量对齐
    const stCls = st === "up" ? "ok" : st === "down" ? "crit" : "muted";
    const lat = c.checked_at ? Math.round(c.latency_ms) + " ms" : "—";
    const latCls = c.checked_at ? "" : "muted";
    const detail = [];
    if (c.type === "http") {
      detail.push(cdItem(I18N.t("form.check_url"), checkTargetDisplay(c), "muted"));
      detail.push(cdItem(I18N.t("form.run_status"), stText, stCls));
      const code = c.status_code || 0;
      detail.push(cdItem(I18N.t("form.status_code"), code ? String(code) : "—", code === 0 ? "muted" : code >= 400 ? "crit" : "ok"));
      detail.push(cdItem(I18N.t("form.response_latency"), lat, latCls));
      if (typeof c.cert_days === "number" && c.cert_days >= 0) {
        const d = c.cert_days;
        detail.push(cdItem(I18N.t("form.cert_remaining"), d + I18N.t("time.days"), d <= 7 ? "crit" : d <= 30 ? "warn" : "ok"));
      }
    } else if (c.type === "tcp") {
      const hp = splitHostPort(c.target);
      detail.push(cdItem(I18N.t("form.target"), hp.host || c.target, "muted"));
      detail.push(cdItem(I18N.t("form.port"), hp.port || "—", ""));
      detail.push(cdItem(I18N.t("form.connect_status"), stText, stCls));
      detail.push(cdItem(I18N.t("form.connect_latency"), lat, latCls));
    } else if (c.type === "ping") {
      detail.push(cdItem(I18N.t("form.check_url"), c.target, "muted"));
      detail.push(cdItem(I18N.t("form.run_status"), stText, stCls));
      const loss = (typeof c.loss_pct === "number" && c.loss_pct >= 0) ? c.loss_pct : null;
      detail.push(cdItem(I18N.t("form.loss_rate"), loss === null ? "—" : Math.round(loss) + "%",
        loss === null ? "muted" : loss === 0 ? "ok" : loss >= 100 ? "crit" : "warn"));
      const hasRtt = c.checked_at && c.latency_ms > 0;
      detail.push(cdItem(I18N.t("form.avg_latency"), hasRtt ? Math.round(c.latency_ms) + " ms" : "—", hasRtt ? "" : "muted"));
    } else if (c.type === "process") {
      const pr = splitProcessTarget(c);
      detail.push(cdItem(I18N.t("form.process_name2"), pr.proc, ""));
      detail.push(cdItem(I18N.t("form.target_host2"), pr.hostName, "muted"));
      detail.push(cdItem(I18N.t("form.run_status"), stText, stCls));
      detail.push(cdItem(I18N.t("form.check_duration"), lat, latCls));
    } else {
      detail.push(cdItem(I18N.t("form.check_url"), checkTargetDisplay(c), "muted"));
      detail.push(cdItem(I18N.t("form.run_status"), stText, stCls));
      detail.push(cdItem(I18N.t("form.latency"), lat, latCls));
    }
    detail.push(cdItem(I18N.t("form.check_interval"), I18N.t("section.every") + c.interval_sec + "s", "muted"));
    detail.push(cdItem(I18N.t("form.last_check"), c.checked_at ? ago(c.checked_at) : I18N.t("ui.not_checked"), "muted"));

    return `<div class="check-card st-${st}" data-id="${esc(c.id)}"${builtin}>
      <div class="check-row-top">
        <span class="st-dot ${st}"></span>
        <span class="ch-name" title="${esc(c.name)}">${esc(c.name)}</span>
        <span class="type-badge t-${esc(c.type)}">${typeText}</span>
        ${builtinTag}
        <span class="st-pill ${st}">${stText}</span>
        ${actions}
      </div>
      <div class="check-detail">${detail.join("")}</div>
      ${(!c.ok && c.checked_at) ? `<div class="check-err">${esc(c.message)}</div>` : ""}
    </div>`;
  }).join("");
}
// 列表 / 胶囊视图切换
function setCheckView(v) {
  CHECK_VIEW = v === "pill" ? "pill" : "list";
  try { localStorage.setItem("aiops_check_view", CHECK_VIEW); } catch (e) {}
  document.querySelectorAll("#checkViewToggle .vt-btn").forEach(b => b.classList.toggle("active", b.dataset.cview === CHECK_VIEW));
  renderChecks(LAST_CHECKS);
}
// 主机 卡片 / 列表 视图切换
function setHostView(v) {
  HOST_VIEW = v === "list" ? "list" : "card";
  try { localStorage.setItem("aiops_host_view", HOST_VIEW); } catch (e) {}
  document.querySelectorAll("#hostViewToggle .vt-btn").forEach(b => b.classList.toggle("active", b.dataset.hview === HOST_VIEW));
  HOST_PAGE = 1;
  renderHosts(LAST_HOSTS);
}
async function loadChecks() {
  try { renderChecks(await fetch(`${API}/checks`).then(r => r.json())); } catch (e) { /* ignore */ }
}

// 把当前拨测监控快照汇总为纯文本供 AI 分析；仅人工采纳/反馈后的结果进入学习闭环。
function checksToText() {
  const checks = LAST_CHECKS || [];
  if (!checks.length) return "（当前没有任何拨测监控项）";
  let down = 0, disabled = 0;
  const lines = checks.map(c => {
    if (c.enabled === false) disabled++;
    const st = !c.enabled ? "已停用" : (c.checked_at ? (c.ok ? "正常" : "异常") : "未探测");
    if (c.enabled && c.checked_at && !c.ok) down++;
    const typeText = c.type === "http" ? "HTTP" : c.type === "tcp" ? "TCP" : c.type === "ping" ? "Ping" : c.type === "process" ? "进程" : (c.type || "");
    let extra = "";
    if (c.type === "http") extra = ` 状态码=${c.status_code || "—"}${(typeof c.cert_days === "number" && c.cert_days >= 0) ? " 证书剩余=" + c.cert_days + "天" : ""}`;
    else if (c.type === "ping" && typeof c.loss_pct === "number" && c.loss_pct >= 0) extra = ` 丢包=${Math.round(c.loss_pct)}%`;
    const lat = c.checked_at ? Math.round(c.latency_ms) + "ms" : "—";
    const err = (c.enabled && c.checked_at && !c.ok && c.message) ? " 错误=" + c.message : "";
    return `- [${typeText}] ${c.name} 目标=${checkTargetDisplay(c)} 状态=${st} 时延=${lat}${extra} 间隔=${c.interval_sec}s${err}`;
  });
  const head = `拨测项共 ${checks.length} 个 · 异常 ${down} 个 · 停用 ${disabled} 个。\n`;
  return (head + lines.join("\n")).slice(0, 12000);
}

// 「🤖 AI 分析」：对当前所有拨测项的可用性/时延/证书/丢包做整体研判，结果自动进入 RAG 记忆闭环
safeAddEventListener("checksAIBtn", "click", () => {
  if (typeof openAIAssist !== "function") { if (typeof toast === "function") toast(I18N.t("assist.unavailable", "AI 面板未就绪"), "err"); return; }
  openAIAssist({ task: "checks_diagnosis", mode: "analyze", title: I18N.t("assist.title_checks", "AI · 拨测监控分析"), context: checksToText() });
});

let CHK_CHARTS = {};
let CHK_HIST = { id: "", name: "", type: "", range: 1, custom: null }; // range=小时数，默认 1h；custom={from,to}
// 自定义监控·历史曲线：复用交互式图表引擎，支持按时间范围筛选 + 自定义绝对区间（与主机趋势图一致）
function openCheckHistory(id, name, type) {
  CHK_HIST = { id, name, type, range: 1, custom: null };
  $("checkHistTitle").textContent = name + " · 监控历史";
  $("checkHistMask").classList.add("show");
  loadCheckHistory();
}
async function loadCheckHistory() {
  const { id, name, type, range, custom } = CHK_HIST;
  const body = $("checkHistBody");
  body.innerHTML = `<div class="empty-line">加载中…</div>`;
  const now = Math.floor(Date.now() / 1000);
  const anchorKey = "checks:" + id;
  const win = (typeof resolveAnchoredRange === "function")
    ? resolveAnchoredRange(anchorKey, range > 0 ? range : 1, custom)
    : { from: custom ? custom.from : (range > 0 ? now - range * 3600 : now - 3600), to: custom ? custom.to : now };
  const from = win.from, to = win.to;
  const load = (typeof beginRangeLoad === "function")
    ? beginRangeLoad(anchorKey)
    : { signal: undefined, isCurrent: () => true };
  // 快捷跨度按钮 + 自定义绝对区间 + 预测 + AI（与主机趋势图一致）
  const ctrl = `${renderChartControls(custom ? -1 : range, "crange")}
    <button class="chip-btn ${custom ? "active" : ""}" data-chk-custom-toggle title="${I18N.t("time.custom_range") || "自定义时间范围"}">${I18N.t("time.custom") || "自定义"}</button>
    ${typeof forecastChipHTML === "function" ? forecastChipHTML("checks") : ""}
    <button type="button" class="chip-btn ai-assist-btn" data-chk-ai title="${I18N.t("hosts.ai_analyze_title", "用 AI 解读该拨测近期趋势")}"><span class="ai-assist-btn-ic">🤖</span>${I18N.t("hosts.ai_analyze", "AI 分析")}</button>
    <span class="chart-custom-range" id="chkCustomPanel"${custom ? "" : " hidden"}>
      <input type="datetime-local" id="chkCustomFrom" class="dt-input" value="${toLocalDatetimeValue(from > 0 ? from : now - 3600)}">
      <span class="dt-sep">→</span>
      <input type="datetime-local" id="chkCustomTo" class="dt-input" value="${toLocalDatetimeValue(to)}">
      <button class="chip-btn primary" data-chk-custom-apply>${I18N.t("time.custom_apply") || "应用"}</button>
    </span>`;
  try {
    const sinceMin = Math.max(1, Math.ceil((to - from) / 60));
    const qs = new URLSearchParams({ since_min: String(sinceMin), from: String(from), to: String(to) });
    const r = await fetch(`${API}/${CHK_HIST.base || "checks"}/${encodeURIComponent(id)}/history?${qs}`,
      load.signal ? { signal: load.signal } : undefined);
    if (!load.isCurrent()) return;
    const all = await r.json().catch(() => []);
    if (!load.isCurrent()) return;
    const pts = (Array.isArray(all) ? all : []).filter(p => p.timestamp >= from && p.timestamp <= to);
    if (!pts.length) {
      body.innerHTML = `<div class="chart-controls">${ctrl}</div><div class="empty-line">该时间范围暂无数据（检查运行一段时间后自动积累，重启后重新计）</div>`;
      return;
    }
    const samples = pts.map(p => ({ timestamp: p.timestamp, latency_ms: p.latency_ms, loss_pct: (typeof p.loss_pct === "number" ? p.loss_pct : null), ok: p.ok }));
    const aligned = typeof alignJoinedSeriesSamples === "function"
      ? alignJoinedSeriesSamples(samples, ["latency_ms", "loss_pct"])
      : samples;
    const isPing = type === "ping";
    const uptime = (pts.filter(p => p.ok).length / pts.length * 100).toFixed(1);
    const avgLat = (pts.reduce((s, p) => s + (p.latency_ms || 0), 0) / pts.length).toFixed(0);
    const span = pts.length > 1 ? fmtDur(pts[pts.length - 1].timestamp - pts[0].timestamp) : I18N.t("time.just_now");
    // 标题已在弹窗头；画布内只用指标副标题，避免「名称 · 延时」重复
    const wrap = (cid, sub) => `<div class="chart-wrap"><div class="chart-sub-title">${esc(sub)}</div><canvas id="${cid}" width="1000" height="240"></canvas>` +
      `<button class="chart-enlarge" data-chart="${cid}" title="${I18N.t('ui.zoom_preview')}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7"/></svg></button></div>`;
    const latSub = (isPing ? I18N.t("form.avg_latency") : I18N.t("form.latency")) + "(" + I18N.t("unit.ms") + ")";
    const chartTitleBase = `${name} · ${latSub}`;
    const reloadBase = {
      checkId: id,
      checkType: type,
      checkName: name,
      base: CHK_HIST.base || "checks",
      mode: "check",
      forecastScope: "checks"
    };
    body.innerHTML = `<div class="chart-controls">${ctrl}</div>
      <div class="chart-container">${wrap("chkLat", latSub)}${isPing ? wrap("chkLoss", I18N.t("form.loss_rate") + "(%)") : ""}</div>
      <div class="hint">采样 ${pts.length} 个 · 时间跨度 ${span} · 可用率 ${uptime}% · 平均延时 ${avgLat} ${I18N.t("unit.ms")} · 悬停查看数值，拖动框选放大，双击还原。</div>`;
    const specs = [
      { id: "chkLat", samples: aligned, series: [
        { key: "latency_ms", label: isPing ? I18N.t("form.avg_latency") : I18N.t("form.latency"), color: "#4c8dff", fmt: v => v.toFixed(0) + " " + I18N.t("unit.ms") },
      ], yMin: 0, yMax: null, opts: {
        title: chartTitleBase, legendMode: "dash", cssH: 220, forecastScope: "checks",
        reload: Object.assign({}, reloadBase)
      } },
    ];
    if (isPing) {
      const lossTitle = `${name} · ${I18N.t("form.loss_rate")}(%)`;
      specs.push({ id: "chkLoss", samples: aligned, series: [
        { key: "loss_pct", label: I18N.t("form.loss_rate"), color: "#f2545b", fmt: v => v.toFixed(0) + "%" },
      ], yMin: 0, yMax: 100, opts: {
        title: lossTitle, legendMode: "dash", cssH: 220, forecastScope: "checks",
        reload: Object.assign({}, reloadBase)
      } });
    }
    if (!load.isCurrent()) return;
    CHK_CHARTS = typeof mountChartsWithForecast === "function"
      ? await mountChartsWithForecast("checks", specs, load)
      : Object.fromEntries(specs.map(sp => [sp.id, createChart(sp.id, sp.samples, sp.series, sp.yMin, sp.yMax, sp.opts)]));
    // 缓存最近一次拨测历史样本，供弹窗内 AI 分析使用
    CHK_HIST.lastSamples = aligned;
    CHK_HIST.lastUptime = uptime;
    CHK_HIST.lastAvgLat = avgLat;
  } catch (e) {
    if (e && (e.name === "AbortError" || /aborted/i.test(String(e.message || e)))) return;
    if (!load.isCurrent()) return;
    body.innerHTML = `<div class="empty-line">加载失败: ${esc(e)}</div>`;
  }
}
document.addEventListener("chart-forecast-toggle", (ev) => {
  if (ev.detail && ev.detail.scope === "checks" && CHK_HIST && CHK_HIST.id) loadCheckHistory();
});
function analyzeCheckHistoryAI() {
  if (typeof openAIAssist !== "function") {
    if (typeof toast === "function") toast(I18N.t("assist.unavailable", "AI 面板未就绪"), "err");
    return;
  }
  const samples = (CHK_HIST && CHK_HIST.lastSamples) || [];
  if (!samples.length) {
    if (typeof toast === "function") toast(I18N.t("empty.no_history", "暂无历史数据"), "err");
    return;
  }
  const { id, name, type } = CHK_HIST;
  const first = samples[0], last = samples[samples.length - 1];
  const t0 = +(first.timestamp || 0), t1 = +(last.timestamp || 0);
  const lats = samples.map(p => +p.latency_ms).filter(v => isFinite(v));
  const avg = lats.length ? (lats.reduce((a, b) => a + b, 0) / lats.length) : 0;
  const peak = lats.length ? Math.max(...lats) : 0;
  const okN = samples.filter(p => p.ok).length;
  const lines = [
    `拨测：${name || id}（id=${id}，类型=${type || "?"}）`,
    `样本数：${samples.length}，时间跨度：约 ${((t1 - t0) / 3600).toFixed(2)} 小时`,
    `可用率：${CHK_HIST.lastUptime != null ? CHK_HIST.lastUptime : (samples.length ? (okN / samples.length * 100).toFixed(1) : "—")}%（成功 ${okN}/${samples.length}）`,
    `延时：均值 ${avg.toFixed(0)} ms · 峰值 ${peak.toFixed(0)} ms · 当前 ${(last.latency_ms || 0).toFixed(0)} ms`,
  ];
  if (type === "ping") {
    const losses = samples.map(p => p.loss_pct).filter(v => typeof v === "number" && isFinite(v));
    if (losses.length) {
      lines.push(`丢包：均值 ${(losses.reduce((a, b) => a + b, 0) / losses.length).toFixed(1)}% · 峰值 ${Math.max(...losses).toFixed(1)}%`);
    }
  }
  lines.push("", "请基于拨测可用性与延时/丢包趋势做根因研判与处置建议，关注尖峰、超时与连续失败。");
  openAIAssist({
    task: "chart_analysis",
    mode: "analyze",
    title: "AI · 拨测趋势 · " + (name || id),
    context: lines.join("\n").slice(0, 12000),
    hint: "正在解读该拨测历史趋势…"
  });
}
// 历史弹窗：时间范围切换（快捷/自定义）+ 图表放大委托
safeAddEventListener("checkHistBody", "click", e => {
  if (e.target.closest("[data-chk-ai]")) { analyzeCheckHistoryAI(); return; }
  const tog = e.target.closest("[data-chk-custom-toggle]");
  if (tog) { const p = $("chkCustomPanel"); if (p) { p.hidden = !p.hidden; if (!p.hidden) { const f = $("chkCustomFrom"); if (f) f.focus(); } } return; }
  if (e.target.closest("[data-chk-custom-apply]")) { applyChkCustomRange(); return; }
  const rb = e.target.closest(".chip-btn[data-crange]");
  if (rb) {
    const next = parseInt(rb.dataset.crange);
    if (CHK_HIST.custom || CHK_HIST.range !== next) {
      if (typeof clearAnchoredRange === "function" && CHK_HIST.id) clearAnchoredRange("checks:" + CHK_HIST.id);
    }
    CHK_HIST.custom = null; CHK_HIST.range = next; loadCheckHistory(); return;
  }
  const en = e.target.closest(".chart-enlarge"); if (!en) return;
  const ch = CHK_CHARTS[en.dataset.chart]; if (ch) openChartZoom(ch);
});
// 读取两个 datetime-local 输入，校验后按自定义绝对区间重新拉取（与主机趋势图一致）
function applyChkCustomRange() {
  applyCustomRangeFromInputs($("chkCustomFrom"), $("chkCustomTo"), (from, to) => {
    CHK_HIST.custom = { from, to };
    loadCheckHistory();
  });
}
async function loadHostsMeta() {
  try { HOST_META = await fetch(`${API}/hosts/meta`).then(r => r.json()); } catch (e) { /* ignore */ }
}
function updateCkTargetLabel() {
  const t = $("ckType").value;
  const adv = $("ckAdvancedWrap"); if (adv) adv.style.display = (t === "http") ? "" : "none"; // 高级模式仅 HTTP
  const dnsF = $("ckDnsFields"); if (dnsF) dnsF.style.display = (t === "dns") ? "" : "none"; // DNS 字段仅 DNS 类型
  if (t === "process") {
    $("ckHostField").style.display = "block";
    $("ckTargetLabel").textContent = I18N.t("form.process_name");
    $("ckTarget").placeholder = I18N.t("form.hint_process");
    return;
  }
  $("ckHostField").style.display = "none";
  if (t === "http") {
    $("ckTargetLabel").textContent = I18N.t("form.url");
    $("ckTarget").placeholder = "https://example.com";
  } else if (t === "ping") {
    $("ckTargetLabel").textContent = I18N.t("form.host_addr");
    $("ckTarget").placeholder = I18N.t("form.hint_url");
  } else if (t === "udp") {
    $("ckTargetLabel").textContent = I18N.t("form.host_port");
    $("ckTarget").placeholder = "127.0.0.1:53";
  } else if (t === "dns") {
    $("ckTargetLabel").textContent = I18N.t("form.domain", "域名");
    $("ckTarget").placeholder = "example.com 或 example.com@8.8.8.8";
  } else {
    $("ckTargetLabel").textContent = I18N.t("form.host_port");
    $("ckTarget").placeholder = "127.0.0.1:3306";
  }
}
function openCheckModal(check) {
  $("checkModalTitle").textContent = check ? I18N.t("ui.edit_check") : I18N.t("ui.add_check");
  $("ckId").value = check ? check.id : "";
  $("ckName").value = check ? check.name : "";
  $("ckType").value = check ? check.type : "http";
  // For process type, extract process name only (not "hostID/procName")
  if (check && check.type === "process") {
    const idx = check.target.indexOf("/");
    $("ckTarget").value = idx > 0 ? check.target.slice(idx + 1) : check.target;
  } else {
    $("ckTarget").value = check ? check.target : "";
  }
  $("ckInterval").value = check ? check.interval_sec : 30;
  $("ckLevel").value = check ? check.level : "critical";
  $("ckEnabled").checked = check ? check.enabled : true;
  // HTTP 高级模式回填
  $("ckAdvanced").checked = !!(check && check.advanced);
  $("ckMethod").value = (check && check.method) || "GET";
  $("ckExpectStatus").value = (check && check.expect_status) ? check.expect_status : "";
  $("ckHeaders").value = (check && check.headers) ? Object.entries(check.headers).map(([k, v]) => `${k}: ${v}`).join("\n") : "";
  $("ckBody").value = (check && check.body) || "";
  $("ckExpectKeyword").value = (check && check.expect_keyword) || "";
  $("ckKeywordRegex").checked = !!(check && check.keyword_is_regex);
  $("ckJsonPath").value = (check && check.json_path) || "";
  $("ckJsonExpect").value = (check && check.json_expect) || "";
  $("ckCertWarnDays").value = (check && check.cert_warn_days) ? check.cert_warn_days : "";
  if ($("ckDnsType")) $("ckDnsType").value = (check && check.dns_type) || "A";
  if ($("ckDnsExpect")) $("ckDnsExpect").value = (check && check.type === "dns") ? (check.expect_keyword || "") : "";
  $("ckAdvancedBody").style.display = $("ckAdvanced").checked ? "" : "none";
  // Populate host select for process type
  populateHostSelect(check);
  updateCkTargetLabel();
  $("checkMask").classList.add("show");
}
function populateHostSelect(check) {
  const sel = $("ckHost");
  sel.innerHTML = `<option value="">-- 选择主机 --</option>` + HOST_META.map(h =>
    `<option value="${esc(h.id)}" ${check && check.target.startsWith(h.id + "/") ? "selected" : ""}>${esc(typeof hostDisplayTitle === "function" ? hostDisplayTitle(h) : ((h.hostname || "") + (h.ip ? " (" + h.ip + ")" : "") || I18N.t("ui.unknown_host", "未知主机")))}</option>`
  ).join("");
}
async function saveCheck() {
  let target = $("ckTarget").value.trim();
  const type = $("ckType").value;
  if (type === "process") {
    const hostId = $("ckHost").value;
    if (!hostId) { toast(I18N.t("valid.select_host"), "err"); return; }
    if (!target) { toast(I18N.t("valid.fill_process"), "err"); return; }
    target = hostId + "/" + target;
  }
  const body = {
    id: $("ckId").value,
    name: $("ckName").value.trim(),
    type: type,
    target: target,
    interval_sec: Math.max(5, parseInt($("ckInterval").value) || 30),
    level: $("ckLevel").value,
    enabled: $("ckEnabled").checked
  };
  if (type === "http" && $("ckAdvanced").checked) { // HTTP 高级模式字段
    body.advanced = true;
    body.method = $("ckMethod").value;
    const hs = {};
    ($("ckHeaders").value || "").split("\n").forEach(line => { const i = line.indexOf(":"); if (i > 0) { const k = line.slice(0, i).trim(); if (k) hs[k] = line.slice(i + 1).trim(); } });
    body.headers = hs;
    body.body = $("ckBody").value;
    body.expect_status = parseInt($("ckExpectStatus").value) || 0;
    body.expect_keyword = $("ckExpectKeyword").value.trim();
    body.keyword_is_regex = $("ckKeywordRegex").checked;
    body.json_path = $("ckJsonPath").value.trim();
    body.json_expect = $("ckJsonExpect").value.trim();
    body.cert_warn_days = parseInt($("ckCertWarnDays").value) || 0;
  }
  if (type === "dns") { // DNS 拨测：记录类型 + 期望包含（复用 expect_keyword 字段）
    body.dns_type = $("ckDnsType").value;
    body.expect_keyword = $("ckDnsExpect").value.trim();
  }
  if (!body.name || !body.target) { toast(I18N.t("valid.fill_name_target"), "err"); return; }
  await withLoading("ckSaveBtn", async () => {
    try {
      const r = await fetch(`${API}/checks`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      if (r.ok) { toast(I18N.t("toast.saved"), "ok"); $("checkMask").classList.remove("show"); loadChecks(); }
      else { const j = await r.json(); toast(I18N.t("toast.save_failed2") + (j.error || ""), "err"); }
    } catch (e) { toast(I18N.t("toast.save_failed2") + e, "err"); }
  });
}
async function delCheck(id) {
  if (!confirm(I18N.t("valid.confirm_delete_check"))) return;
  try {
    const r = await fetch(`${API}/checks/${encodeURIComponent(id)}`, { method: "DELETE" });
    if (r.ok) { toast(I18N.t("toast.deleted"), "ok"); loadChecks(); } else { toast(I18N.t("toast.delete_failed"), "err"); }
  } catch (e) { toast(I18N.t("toast.deleted") + ": " + e, "err"); }
}

/* ---------- 账户 / 个人信息 ---------- */
let CUR_ROLE = "";
const roleLabel = r => ({ admin: I18N.t("ui.admin"), operator: I18N.t("ui.operator"), viewer: I18N.t("ui.readonly") }[r] || r || "");
const canWrite = () => CUR_ROLE === "operator" || CUR_ROLE === "admin";
const isAdmin = () => CUR_ROLE === "admin";
function setUser(me) {
  const name = me.display_name || me.username || I18N.t("ui.user");
  const initial = (name[0] || "A");
  const roleLabels = { admin: "管理员", operator: "操作员", viewer: "查看者" };
  // 顶栏按钮
  var el = $("userName"); if (el) el.textContent = name;
  el = $("userAvatar"); if (el) el.textContent = initial;
  // 下拉菜单大图
  el = $("userNameLg"); if (el) el.textContent = name;
  el = $("userAvatarLg"); if (el) el.textContent = initial;
  el = $("userRoleLg"); if (el) el.textContent = roleLabels[me.role] || me.role || "—";
  if (me.role) {
    CUR_ROLE = me.role;
    document.body.dataset.role = me.role;
    document.querySelectorAll('.nav-group[data-group="security"]').forEach(g => {
      g.style.display = me.role === "viewer" ? "none" : "";
    });
  }
}
// fetchWithTimeout wraps fetch with an AbortController timeout so mobile
// browsers on slow/unstable networks don't hang indefinitely. Returns the
// Response or throws an AbortError / network error.
function fetchWithTimeout(url, opts, timeoutMs) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs || 15000);
  return fetch(url, Object.assign({}, opts, { signal: ctrl.signal })).finally(() => clearTimeout(timer));
}
async function initAuth() {
  try {
    const r = await fetchWithTimeout(`${API}/me`, {}, 10000);
    if (r.ok) {
      const me = await r.json();
      setUser(me);
      $("loginView").classList.remove("show");
      startApp();
      // v5.4.0: force password change if admin reset was used
      if (me.must_change_password) {
        // 强制进入「安全初始化」弹窗：需修改用户名 + 密码后方可进入控制台
        setTimeout(() => openInitSetup(), 300);
      }
    }
    else { $("loginView").classList.add("show"); }
  } catch (e) {
    // Network error on initial auth check — show login with a friendly hint
    // instead of a raw "Failed to fetch" that confuses mobile users.
    $("loginView").classList.add("show");
    const loginErrEl = $("loginErr");
    if (loginErrEl) loginErrEl.textContent = I18N.t("toast.network_check_failed");
  }
}
/* ---------- 消息中心（顶栏铃铛 + 未读徽标 + 下拉面板） ---------- */
let MSG_POLL = null;
let MSG_SEEN_MAX = -1; // 已见最大消息 id：首次加载只记基线不弹窗，之后新消息才弹 toast
function msgEsc(s){return String(s==null?"":s).replace(/[&<>"']/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));}
function initMsgCenter() {
  const panel = $("notifPanel"), wrap = $("notifWrap");
  if (!$("notifBtn") || !panel) return;
  safeAddEventListener("notifBtn", "click", (e) => {
    e.stopPropagation();
    const open = panel.classList.toggle("show");
    if (open) loadMessages();
  });
  safeAddEventListener("notifReadAll", "click", async (e) => {
    e.stopPropagation();
    try { await fetch(`${API}/messages/read-all`, { method: "POST" }); } catch (_) {}
    loadMessages();
  });
  document.addEventListener("click", (e) => { if (wrap && !wrap.contains(e.target)) panel.classList.remove("show"); });
  loadMessages();
  if (MSG_POLL) clearInterval(MSG_POLL);
  MSG_POLL = setInterval(loadMessages, 20000);
}
async function loadMessages() {
  try {
    const data = await fetch(`${API}/messages?limit=50`).then(r => r.json());
    const msgs = data.messages || [];
    const unread = data.unread || 0;
    const badge = $("notifBadge");
    if (badge) {
      if (unread > 0) { badge.textContent = unread > 99 ? "99+" : unread; badge.style.display = ""; }
      else badge.style.display = "none";
    }
    renderMessages(msgs);
    // 新消息弹窗提醒（仅对仪表盘类消息，如 AI 后台生成完成）；首次加载只记基线，不刷屏。
    const maxId = msgs.reduce((m, x) => Math.max(m, x.id || 0), 0);
    if (MSG_SEEN_MAX >= 0) {
      msgs.filter(x => (x.id || 0) > MSG_SEEN_MAX && !x.read && x.view === "dashboards")
        .sort((a, b) => (a.id || 0) - (b.id || 0)).slice(-3)
        .forEach(x => toast(x.title || "新消息", (x.level === "warning" || x.level === "critical") ? "err" : "ok"));
    }
    MSG_SEEN_MAX = Math.max(MSG_SEEN_MAX, maxId);
  } catch (_) {}
}
function renderMessages(msgs) {
  const list = $("notifList"), empty = $("notifEmpty");
  if (!list) return;
  if (empty) empty.style.display = msgs.length ? "none" : "";
  const pad = n => String(n).padStart(2, "0");
  list.innerHTML = msgs.map(m => {
    const t = new Date((m.ts || 0) * 1000);
    const ts = `${t.getMonth()+1}-${pad(t.getDate())} ${pad(t.getHours())}:${pad(t.getMinutes())}`;
    return `<div class="notif-item ${m.read ? "" : "unread"}" data-id="${m.id}" data-view="${msgEsc(m.view || "")}" data-ref="${msgEsc(m.ref || "")}">
      <span class="notif-dot ${msgEsc(m.level || "info")}"></span>
      <div class="notif-body">
        <div class="notif-title">${msgEsc(m.title || "")}</div>
        ${m.body ? `<div class="notif-sub">${msgEsc(m.body)}</div>` : ""}
        <div class="notif-time">${ts}</div>
      </div></div>`;
  }).join("");
  list.querySelectorAll(".notif-item").forEach(el => {
    el.addEventListener("click", async () => {
      const id = parseInt(el.dataset.id, 10);
      const view = el.dataset.view, ref = el.dataset.ref;
      try { await fetch(`${API}/messages/read`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ids: [id] }) }); } catch (_) {}
      const p = $("notifPanel"); if (p) p.classList.remove("show");
      if (view && typeof switchView === "function") switchView(view);
      // 仪表盘消息带看板 id：切到仪表盘视图后直接打开该看板。
      if (view === "dashboards" && ref && typeof openDashboard === "function") setTimeout(() => openDashboard(ref), 60);
      loadMessages();
    });
  });
}
function startApp() {
  if (APP_STARTED) return;
  APP_STARTED = true;
  initTheme();
  initNotifications();
  initMsgCenter();
  showSkeleton();
  refresh(); loadChecks();
  // P1-2: 差异化轮询频率 — 按当前视图 + 标签页可见性调整刷新间隔
  const POLL_BASE = 5000;
  let pollTimer = null;
  function schedulePoll() {
    if (pollTimer) clearTimeout(pollTimer);
    const view = document.querySelector(".view.active")?.id.replace("view-", "") || "overview";
    const intervals = {
      overview: 5000, hosts: 5000, checks: 10000, alerts: 5000,
      automation: 15000, forward: 15000, log: 10000,
      dashboard: 20000, sre: 15000, security: 15000,
      "host-security": 15000, "web-security": 15000,
      k8s: 20000, sql: 20000, containers: 20000, hyperv: 20000,
      netflow: 20000, snmp: 20000, hardware: 20000
    };
    let interval = intervals[view] || 20000;
    // 后台标签页降频至 15s，减少不必要的网络请求和 DOM 渲染
    if (document.visibilityState === "hidden") interval = Math.max(interval, 15000);
    pollTimer = setTimeout(() => {
      refresh();
      // 只在拨测视图激活时轮询/重建拨测网格，避免其它页面每个周期都全量重建隐藏的 check 卡片。
      if (document.querySelector("#view-checks.active")) loadChecks();
      if (document.querySelector("#view-forward.active")) loadForwards();
      schedulePoll();
    }, interval);
  }
  schedulePoll();
  // 视图切换时立即调整轮询频率
  document.querySelectorAll(".nav-item").forEach(n => n.addEventListener("click", () => setTimeout(schedulePoll, 100)));
  // 标签页可见性变化时重排轮询
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") { refresh(true); schedulePoll(); }
  });
  // P3-1: 初始化 WebSocket 推送（带降级到轮询）
  initPushWS();
  // 应用 #... 深链（含首屏进入；兼容旧 ?ui=legacy）
  try { if (typeof applyLegacyHashRoute === "function") applyLegacyHashRoute(); } catch (e) {}
}
// 首次登录 · 安全初始化：强制修改用户名 + 密码的专用弹窗（替代直接打开个人信息页）。
// 弹窗带 data-forced，无法通过 ESC / 点遮罩 / ✕ 关闭；完成后会话重签并刷新进入。
async function openInitSetup() {
  try {
    const me = await fetch(`${API}/me`).then(r => r.json()).catch(() => ({}));
    const u = $("initUser"); if (u) u.value = me.username || "";
    const p = $("initPass"); if (p) p.value = "";
    const p2 = $("initPass2"); if (p2) p2.value = "";
    const err = $("initErr"); if (err) { err.textContent = ""; err.style.display = "none"; }
    const mask = $("initSetupMask"); if (mask) mask.classList.add("show");
    if (u) setTimeout(() => u.focus(), 60);
  } catch (e) { toast(I18N.t("toast.read_failed2") + e, "err"); }
}
async function submitInitSetup() {
  const err = $("initErr");
  const showErr = (m) => { if (err) { err.textContent = m; err.style.display = "block"; } else toast(m, "err"); };
  if (err) { err.textContent = ""; err.style.display = "none"; }
  const uname = ($("initUser").value || "").trim();
  const pw = $("initPass").value || "";
  const pw2 = $("initPass2").value || "";
  if (!uname) { showErr(I18N.t("init.err_username", "请输入登录用户名")); return; }
  if (!pw) { showErr(I18N.t("init.err_password", "请输入新密码")); return; }
  if (pw !== pw2) { showErr(I18N.t("init.err_mismatch", "两次输入的密码不一致")); return; }
  if (pw.length < 8) { showErr(I18N.t("auth.password_policy", "密码需至少 8 位，含大小写字母、数字和特殊字符")); return; }
  await withLoading($("initSubmitBtn"), async () => {
    try {
      const r = await fetch(`${API}/account/init`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: uname, password: pw })
      });
      const j = await r.json().catch(() => ({}));
      if (r.ok) {
        const mask = $("initSetupMask"); if (mask) mask.classList.remove("show");
        // 后端已清除会话并要求重新登录（relogin:true）：不再进入控制台，
        // 而是提示并跳转到登录页，强制用新的用户名/密码重新登录。
        toast(I18N.t("init.relogin", "初始化完成，请用新的用户名和密码重新登录"), "ok");
        setTimeout(() => location.reload(), 1000);
      } else {
        showErr(j.error || I18N.t("toast.save_failed"));
      }
    } catch (e) { showErr(I18N.t("toast.save_failed2") + e); }
  });
}
async function openProfile(tab) {
  try {
    const me = await fetch(`${API}/me`).then(r => r.json());
    $("pfUsername").value = me.username || "";
    $("pfDisplay").value = me.display_name || "";
    $("pfEmail").value = me.email || "";
    $("pfPhone").value = me.phone || "";
    $("pfOld").value = ""; $("pfNew").value = "";
    setUser(me); // 用最新 /me 刷新顶栏与 CUR_ROLE（角色可能已变更）
    // 清空各 Tab 内联错误
    ["pfProfileErr", "pfPwdErr", "pfTermPwdErr"].forEach(id => { const e = $(id); if (e) { e.textContent = ""; e.style.display = "none"; } });
    renderMfaState(!!me.mfa_enabled);
    // v5.3.0: 加载终端密码状态
    loadTermPwdStatus();
    $("profileMask").classList.add("show");
    // 切换到底层请求指定的 Tab（默认「个人信息」）；非管理员无法进入用户管理 / 数据与备份
    const adminTabs = { users:1, ops:1 };
    const target = (adminTabs[tab] && !isAdmin()) ? "info" : (tab || "info");
    switchProfileTab(target);
  } catch (e) { toast(I18N.t("toast.read_failed2") + e, "err"); }
}
let PROFILE_TAB = "info";
let PROFILE_USERS_LOADED = false;
let PROFILE_OPS_LOADED = false;
async function switchProfileTab(tab) {
  PROFILE_TAB = tab;
  document.querySelectorAll("#profileTabs .tab").forEach(b => b.classList.toggle("active", b.dataset.ptab === tab));
  document.querySelectorAll("#profileMask .tab-panel").forEach(p => p.classList.toggle("active", p.id === "tab-profile-" + tab));
  // 用户管理 Tab：首次进入时按需独立加载（保持其它 Tab 状态不重渲染）
  if (tab === "users" && isAdmin() && !PROFILE_USERS_LOADED) {
    PROFILE_USERS_LOADED = true;
    await loadUsers();
  }
  if (tab === "ops" && isAdmin()) {
    await loadOpsAdmin();
    PROFILE_OPS_LOADED = true;
  }
  if (tab === "sso" && typeof loadProfileSSOBindings === "function") {
    await loadProfileSSOBindings();
  }
}
async function saveProfile() {
  const errEl = $("pfProfileErr");
  if (errEl) { errEl.textContent = ""; errEl.style.display = "none"; }
  try {
    const uname = $("pfUsername").value.trim();
    const r = await fetch(`${API}/profile`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: uname, display_name: $("pfDisplay").value.trim(), email: $("pfEmail").value.trim(), phone: $("pfPhone").value.trim() })
    });
    const j = await r.json().catch(() => ({}));
    if (r.ok) { toast(I18N.t("toast.profile_saved"), "ok"); setUser({ display_name: $("pfDisplay").value.trim(), username: j.username || uname }); }
    else if (errEl) { errEl.textContent = j.error || I18N.t("toast.save_failed"); errEl.style.display = "block"; }
    else toast(j.error || I18N.t("toast.save_failed"), "err");
  } catch (e) { toast(I18N.t("toast.save_failed2") + e, "err"); }
}
async function changePassword() {
  const errEl = $("pfPwdErr");
  if (errEl) { errEl.textContent = ""; errEl.style.display = "none"; }
  if (!$("pfOld").value || !$("pfNew").value) {
    if (errEl) { errEl.textContent = I18N.t("valid.fill_passwords"); errEl.style.display = "block"; }
    else toast(I18N.t("valid.fill_passwords"), "err");
    return;
  }
  if (!pwPolicyOK($("pfNew").value)) {
    if (errEl) { errEl.textContent = I18N.t("auth.password_policy"); errEl.style.display = "block"; }
    else toast(I18N.t("auth.password_policy"), "err");
    return;
  }
  try {
    const r = await fetch(`${API}/password`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ old: $("pfOld").value, new: $("pfNew").value })
    });
    const j = await r.json();
    if (r.ok) { toast(I18N.t("toast.password_changed"), "ok"); $("pfOld").value = ""; $("pfNew").value = ""; }
    else if (errEl) { errEl.textContent = j.error || I18N.t("toast.update_failed"); errEl.style.display = "block"; }
    else toast(j.error || I18N.t("toast.update_failed"), "err");
  } catch (e) { toast(I18N.t("toast.update_failed2") + e, "err"); }
}

/* ===================== v5.3.0: 终端密码管理（个人信息页） ===================== */
let TERM_PWD_CHANGE_SHOWING = false;

async function loadTermPwdStatus() {
  try {
    const r = await fetch("/api/user/terminal-password/status", { credentials: "include" });
    const j = await r.json().catch(() => ({}));
    const valEl = $("pfTermPwdStatusVal");
    if (valEl) {
      if (j.has_password) {
        valEl.textContent = I18N.t("term_auth.password_set");
        valEl.className = "term-pwd-status-val set";
      } else {
        valEl.textContent = I18N.t("term_auth.no_password_set");
        valEl.className = "term-pwd-status-val unset";
      }
    }
  } catch (e) { /* 静默失败 */ }
}

function toggleTermPwdChange() {
  TERM_PWD_CHANGE_SHOWING = !TERM_PWD_CHANGE_SHOWING;
  const authField = $("pfTermPwdAuthField");
  const newField = $("pfTermPwdNewField");
  const errEl = $("pfTermPwdErr");
  const btn = $("pfTermPwdBtn");

  if (TERM_PWD_CHANGE_SHOWING) {
    // 显示修改表单
    $("pfTermPwdAuth").value = "";
    $("pfTermPwdNew").value = "";
    if (errEl) { errEl.textContent = ""; errEl.style.display = "none"; }
    if (authField) authField.style.display = "block";
    if (newField) newField.style.display = "block";
    if (btn) btn.textContent = I18N.t("ui.cancel");
    // 根据 MFA 状态调整验证字段标签
    const authLabel = $("pfTermPwdAuthLabel");
    if (authLabel) {
      authLabel.textContent = MFA_ENABLED ? I18N.t("term_auth.mfa_code") : I18N.t("term_auth.current_password");
    }
    $("pfTermPwdAuth").placeholder = MFA_ENABLED ? I18N.t("mfa.code_6") : "";
    $("pfTermPwdAuth").maxLength = MFA_ENABLED ? 6 : 524288;
  } else {
    // 隐藏修改表单
    if (authField) authField.style.display = "none";
    if (newField) newField.style.display = "none";
    if (errEl) { errEl.textContent = ""; errEl.style.display = "none"; }
    if (btn) btn.textContent = I18N.t("term_auth.change_password_btn");
  }
}

async function submitTermPwdChange() {
  if (!TERM_PWD_CHANGE_SHOWING) {
    toggleTermPwdChange();
    return;
  }
  const code = MFA_ENABLED
    ? String($("pfTermPwdAuth").value || "").replace(/\D/g, "").slice(0, 6)
    : $("pfTermPwdAuth").value.trim();
  const newPwd = $("pfTermPwdNew").value.trim();
  const errEl = $("pfTermPwdErr");

  if (!code || !newPwd) {
    if (errEl) { errEl.textContent = I18N.t("term_auth.fill_verify_password"); errEl.style.display = "block"; }
    return;
  }

  try {
    const r = await fetch("/api/user/terminal-password/set", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password: newPwd, code: code })
    });
    const j = await r.json().catch(() => ({}));

    if (r.ok) {
      toast(I18N.t("term_auth.changed_ok"), "ok");
      toggleTermPwdChange(); // 收起表单
      loadTermPwdStatus();   // 刷新状态
    } else {
      if (j.mfa_required) {
        // 修改时需要 MFA，但未提供
        if (errEl) { errEl.textContent = I18N.t("term_auth.enter_mfa_code"); errEl.style.display = "block"; }
        return;
      }
      if (j.code === "totp_replay" || j.code === "totp_invalid") {
        const authEl = $("pfTermPwdAuth");
        if (authEl) { authEl.value = ""; authEl.focus(); }
      }
      if (errEl) { errEl.textContent = j.error || I18N.t("toast.update_failed"); errEl.style.display = "block"; }
    }
  } catch (e) {
    if (errEl) { errEl.textContent = I18N.t("toast.network_error"); errEl.style.display = "block"; }
  }
}

/* ===================== 两步验证（TOTP / Google Authenticator） ===================== */
let MFA_ENABLED = false;
function renderMfaState(enabled) {
  MFA_ENABLED = enabled;
  const st = $("mfaState"), chk = $("mfaToggleChk");
  if (st) { st.textContent = enabled ? I18N.t("toast.enabled") : I18N.t("toast.disabled"); st.className = "mfa-state " + (enabled ? "on" : "off"); }
  if (chk) { chk.checked = enabled; }
}
async function openMfaSetup(forced) {
  const body = $("mfaBody");
  $("mfaTitle").textContent = forced ? I18N.t("ui.mfa_required") : I18N.t("ui.enable_mfa");
  body.innerHTML = `<div class="empty-line">正在生成密钥…</div>`;
  $("mfaMask").classList.add("show");
  let data;
  try { data = await fetch(`${API}/mfa/setup`, { method: "POST" }).then(r => r.json()); }
  catch (e) { body.innerHTML = `<div class="empty-line">生成失败：${esc(e)}</div>`; return; }
  const secret = data.secret || "", qrURI = data.qr_datauri || "";
  const grp = secret.replace(/(.{4})/g, "$1 ").trim();
  body.innerHTML = `
    ${forced ? `<div class="mfa-desc" style="margin-bottom:10px;color:var(--warn-txt,#f2c078)">管理员已启用全局两步验证策略，请完成绑定后登录。</div>` : ""}
    <ol class="mfa-steps">
      <li>打开 <b>Google Authenticator</b>（或任意 TOTP 应用），扫描二维码；无法扫码时可手动输入下方密钥。</li>
      <li>输入应用当前显示的 6 位动态口令，点「确认启用」。</li>
    </ol>
    <div class="mfa-qr" id="mfaQr"></div>
    <div class="mfa-secret">${I18N.t("mfa.secret_label")}　<code class="mono" id="mfaSecret">${esc(grp)}</code><button class="btn ghost sm" id="mfaCopy" type="button">${I18N.t("mfa.copy_btn")}</button></div>
    <div class="field"><label>${I18N.t("form.totp_code")}</label><input type="text" id="mfaCode" inputmode="numeric" maxlength="6" placeholder="${I18N.t('mfa.code_6')}" autocomplete="one-time-code"></div>
    <div class="login-err" id="mfaErr"></div>
    <div class="mfa-foot"><button class="btn primary" id="mfaConfirm" type="button">${I18N.t("mfa.confirm_enable")}</button></div>`;
  if (qrURI) $("mfaQr").innerHTML = `<img src="${esc(qrURI)}" alt="MFA QR Code" class="qr-img">`;
  else $("mfaQr").innerHTML = `<div class="mfa-desc">二维码不可用，请在应用中手动输入上方密钥。</div>`;
  $("mfaCopy").onclick = () => { try { navigator.clipboard.writeText(secret); toast(I18N.t("toast.secret_copied"), "ok"); } catch (_) { } };
  $("mfaConfirm").onclick = async () => {
    const errEl = $("mfaErr"); errEl.textContent = "";
    const code = $("mfaCode").value.trim();
    if (code.length !== 6) { errEl.textContent = I18N.t("valid.enter_totp"); return; }
    const r = await fetch(`${API}/mfa/enable`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ secret, code }) });
    const j = await r.json().catch(() => ({}));
    if (r.ok) {
      toast(I18N.t("toast.mfa_enabled"), "ok");
      $("mfaMask").classList.remove("show");
      if (forced) {
        // Global MFA enforcement: complete login after enrollment.
        setUser(await fetch(`${API}/me`).then(x => x.json()));
        const lv = $("loginView"); if (lv) lv.classList.remove("show");
        startApp();
      } else { renderMfaState(true); }
    }
    else errEl.textContent = j.error || I18N.t("toast.enable_failed");
  };
  setTimeout(() => { const el = $("mfaCode"); if (el) el.focus(); }, 60);
}
function openMfaDisable() {
  const body = $("mfaBody");
  $("mfaTitle").textContent = I18N.t("ui.disable_mfa");
  body.innerHTML = `
    <div class="mfa-desc" style="margin-bottom:14px">关闭后，登录将不再需要动态口令。请选择验证方式：</div>
    <div class="field"><label>${I18N.t("form.password")}</label><input type="password" id="mfaPass" autocomplete="current-password"></div>
    <div class="login-err" id="mfaErr"></div>
    <div class="mfa-foot">
      <button class="btn danger" id="mfaConfirmOff" type="button">${I18N.t("mfa.disable_pwd")}</button>
      <button class="btn" id="mfaEmailUnbind" type="button">${I18N.t("mfa.email_unbind_btn")}</button>
    </div>`;
  $("mfaMask").classList.add("show");
  $("mfaConfirmOff").onclick = async () => {
    const errEl = $("mfaErr"); errEl.textContent = "";
    const r = await fetch(`${API}/mfa/disable`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ password: $("mfaPass").value }) });
    const j = await r.json().catch(() => ({}));
    if (r.ok) { toast(I18N.t("toast.mfa_disabled"), "ok"); $("mfaMask").classList.remove("show"); renderMfaState(false); }
    else errEl.textContent = j.error || I18N.t("toast.disable_failed");
  };
  $("mfaEmailUnbind").onclick = () => openMfaEmailUnbind();
  setTimeout(() => { const el = $("mfaPass"); if (el) el.focus(); }, 60);
}

/* ---------- 通过邮箱验证码解除 MFA ---------- */
function openMfaEmailUnbind() {
  const body = $("mfaBody");
  $("mfaTitle").textContent = I18N.t("ui.unbind_mfa_email");
  body.innerHTML = `
    <div class="mfa-desc" style="margin-bottom:14px">系统将向已绑定邮箱发送 6 位验证码，验证通过后关闭两步验证。</div>
    <div class="login-err" id="mfaErr"></div>
    <div class="mfa-foot">
      <button class="btn primary" id="mfaSendCode" type="button">${I18N.t("mfa.send_code_btn")}</button>
      <span style="flex:1"></span>
    </div>
    <div class="field" id="mfaCodeRow" style="display:none">
      <label>${I18N.t("form.email_code")}</label>
      <input type="text" id="mfaEmailCode" inputmode="numeric" maxlength="6" placeholder="${I18N.t('mfa.code_6_v2')}" autocomplete="one-time-code">
    </div>
    <div class="mfa-foot" id="mfaVerifyRow" style="display:none">
      <button class="btn danger" id="mfaConfirmEmailUnbind" type="button">${I18N.t("mfa.confirm_unbind")}</button>
    </div>`;
  $("mfaMask").classList.add("show");
  $("mfaSendCode").onclick = async () => {
    const errEl = $("mfaErr"); errEl.textContent = "";
    const r = await fetch(`${API}/mfa/unbind-via-email`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action: "send_code" }) });
    const j = await r.json().catch(() => ({}));
    if (r.ok) {
      toast(I18N.t("toast.code_sent"), "ok");
      $("mfaSendCode").textContent = I18N.t("ui.resend");
      $("mfaSendCode").disabled = true;
      setTimeout(() => { const b = $("mfaSendCode"); if (b) { b.disabled = false; } }, 60000);
      $("mfaCodeRow").style.display = "";
      $("mfaVerifyRow").style.display = "";
      setTimeout(() => { const el = $("mfaEmailCode"); if (el) el.focus(); }, 60);
    } else {
      errEl.textContent = j.error || I18N.t("toast.send_failed");
    }
  };
  $("mfaConfirmEmailUnbind").onclick = async () => {
    const errEl = $("mfaErr"); errEl.textContent = "";
    const code = $("mfaEmailCode").value.trim();
    if (code.length !== 6) { errEl.textContent = I18N.t("valid.enter_code"); return; }
    const r = await fetch(`${API}/mfa/unbind-via-email`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action: "verify", code }) });
    const j = await r.json().catch(() => ({}));
    if (r.ok) { toast(I18N.t("toast.mfa_unbind_email"), "ok"); $("mfaMask").classList.remove("show"); renderMfaState(false); }
    else errEl.textContent = j.error || I18N.t("toast.unbind_failed");
  };
}

/* ---------- 用户管理（管理员）---------- */
async function openUsers() {
  // 用户管理已并入「个人信息」四 Tab 布局中的「用户管理」分页
  openProfile("users");
}
async function loadUsers() {
  // Fetch global MFA policy status
  try {
    const gm = await fetch(`${API}/mfa/global`).then(r => r.json());
    const chk = $("globalMfaChk");
    if (chk) { chk.checked = !!gm.mfa_required; chk.disabled = false; }
  } catch (_) { /* non-admin or error — switch stays disabled */ }
  const list = $("usersList");
  list.innerHTML = `<div class="empty-line">加载中…</div>`;
  let users;
  try { users = await fetch(`${API}/users`).then(r => r.json()); }
  catch (e) { list.innerHTML = `<div class="empty-line">加载失败: ${esc(e)}</div>`; return; }
  if (!Array.isArray(users) || !users.length) { list.innerHTML = `<div class="empty-line">${I18N.t("empty.no_users")}</div>`; return; }
  list.innerHTML = users.map(u => `
    <div class="user-row" data-name="${esc(u.username)}">
      <div class="user-info">
        <div class="user-main"><span class="user-name">${esc(u.username)}</span>
          <span class="role-badge role-${esc(u.role)}">${roleLabel(u.role)}</span>
          ${u.mfa_enabled ? `<span class="user-mfa" title="${I18N.t('mfa.enabled_badge')}">${I18N.t('mfa.enabled_badge')}</span>` : ""}</div>
        <div class="user-sub">${esc(u.display_name || "—")}${u.email ? " · " + esc(u.email) : ""}</div>
      </div>
      <div class="user-acts">
        <button class="btn ghost sm" data-act="edit">${I18N.t("ui.edit")}</button>
        <button class="btn ghost sm" data-act="pwd">${I18N.t("ui.reset_password")}</button>
        ${u.mfa_enabled ? `<button class="btn ghost sm" data-act="mfa">${I18N.t("ui.unbind_mfa")}</button>` : ""}
        <button class="btn ghost sm ubtn-del" data-act="del">${I18N.t("ui.delete")}</button>
      </div>
    </div>`).join("");
}
function openUserEdit(user) {
  const isNew = !user;
  $("userEditTitle").textContent = isNew ? I18N.t("ui.new_user") : I18N.t("ui.edit_user") + user.username;
  const roleOpts = ["admin", "operator", "viewer"].map(r => `<option value="${r}" ${user && user.role === r ? "selected" : ""}>${roleLabel(r)}</option>`).join("");
  const folders = user ? (user.allowed_folder_ids || []).join(",") : "";
  const hosts = user ? (user.allowed_host_ids || []).join(",") : "";
  const tags = user ? (user.allowed_tags || []).join(",") : "";
  $("userEditBody").innerHTML = `
    ${isNew ? `<div class="field"><label>${I18N.t("form.username")}</label><input type="text" id="ueName" placeholder="${I18N.t('form.username_format')}"></div>
    <div class="field"><label>${I18N.t("form.initial_password")}</label><input type="password" id="uePass"></div>` : ""}
    <div class="field"><label>${I18N.t("form.display_name")}</label><input type="text" id="ueDisplay" value="${user ? esc(user.display_name || "") : ""}" placeholder="${I18N.t('form.hint_display_name')}"></div>
    <div class="field"><label>${I18N.t("form.email_optional")}</label><input type="text" id="ueEmail" value="${user ? esc(user.email || "") : ""}" placeholder="name@example.com"></div>
    <div class="field"><label>${I18N.t("form.role")}</label><div class="select-wrap"><select id="ueRole">${roleOpts}</select></div></div>
    ${!isNew ? `<div class="field"><label>授权主机组文件夹 ID（逗号分隔，空=不限）</label><input type="text" id="ueFolders" class="mono" value="${esc(folders)}" placeholder="hf-xxxx"></div>
    <div class="field"><label>授权主机 ID（逗号分隔，空=不限）</label><input type="text" id="ueHosts" class="mono" value="${esc(hosts)}"></div>
    <div class="field"><label>授权标签/分类（逗号分隔）</label><input type="text" id="ueTags" value="${esc(tags)}" placeholder="生产,DB"></div>` : ""}
    <div class="login-err" id="ueErr"></div>
    <div class="mfa-foot"><button class="btn primary" id="ueSave" type="button">${isNew ? I18N.t("ui.create_user") : I18N.t("ui.save")}</button></div>`;
  $("userEditMask").classList.add("show");
  $("ueSave").onclick = async () => {
    const errEl = $("ueErr"); errEl.textContent = "";
    const splitCSV = (id) => (($(id) || {}).value || "").split(",").map(s => s.trim()).filter(Boolean);
    const body = { display_name: $("ueDisplay").value.trim(), email: $("ueEmail").value.trim(), role: $("ueRole").value };
    let r;
    if (isNew) {
      body.username = $("ueName").value.trim();
      body.password = $("uePass").value;
      r = await fetch(`${API}/users`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    } else {
      body.scope_set = true;
      body.allowed_folder_ids = splitCSV("ueFolders");
      body.allowed_host_ids = splitCSV("ueHosts");
      body.allowed_tags = splitCSV("ueTags");
      r = await fetch(`${API}/users/${encodeURIComponent(user.username)}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    }
    const j = await r.json().catch(() => ({}));
    if (r.ok) { toast(isNew ? I18N.t("toast.user_created") : I18N.t("toast.saved"), "ok"); $("userEditMask").classList.remove("show"); loadUsers(); }
    else errEl.textContent = j.error || I18N.t("toast.operation_failed");
  };
}
async function usersAction(name, act) {
  if (act === "del") {
    // 两步确认：防止误删敏感操作
    if (!confirm(`⚠ 确定删除用户「${name}」？\n\n该操作不可撤销，该用户的所有会话将立即失效。\n如需继续，请点击「确定」。`)) return;
    const r = await fetch(`${API}/users/${encodeURIComponent(name)}`, { method: "DELETE" });
    const j = await r.json().catch(() => ({}));
    if (r.ok) { toast(I18N.t("toast.user_deleted"), "ok"); loadUsers(); } else toast(j.error || I18N.t("toast.delete_failed"), "err");
  } else if (act === "pwd") {
    const pass = await requestAITextInput({
      title:"重置用户密码",message:`为「${name}」设置新密码。`,
      label:"新密码（至少 8 位）",placeholder:"输入符合安全策略的新密码",
      submitLabel:"重置密码",inputType:"password",singleLine:true,autocomplete:"new-password",
      maxLength:256,danger:false,requiredMessage:"请输入新密码"
    });
    if (pass == null) return;
    if (!pwPolicyOK(pass.trim())) { toast(I18N.t("auth.password_policy"), "err"); return; }
    const r = await fetch(`${API}/users/${encodeURIComponent(name)}/reset-password`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ password: pass }) });
    const j = await r.json().catch(() => ({}));
    if (r.ok) toast(I18N.t("toast.password_reset"), "ok"); else toast(j.error || I18N.t("toast.reset_failed"), "err");
  } else if (act === "mfa") {
    if (!confirm(`确定解除「${name}」的两步验证绑定？`)) return;
    const r = await fetch(`${API}/users/${encodeURIComponent(name)}/reset-mfa`, { method: "POST" });
    const j = await r.json().catch(() => ({}));
    if (r.ok) { toast(I18N.t("toast.mfa_unbound"), "ok"); loadUsers(); } else toast(j.error || I18N.t("toast.operation_failed"), "err");
  }
}

/* ---------- 账户找回：用户名 / 密码 ---------- */
// New dual-verification flow (email code + optional MFA TOTP)
function openRecoverUser(e) { if (e) e.preventDefault(); showRecoverFlow('recover_username'); }
function openRecoverPass(e) { if (e) e.preventDefault(); showRecoverFlow('recover_password'); }

function showRecoverFlow(purpose) {
  const body = $("recoverBody");
  $("recoverTitle").textContent = I18N.t("recover.title");
  const label = purpose === 'recover_username' ? I18N.t("login.forgot_user") : I18N.t("login.forgot_pass");
  body.innerHTML = `
    <div class="mfa-desc" style="margin-bottom:14px">${I18N.t("recover.enter_email_desc")}</div>
    <div class="field"><label>${I18N.t("form.email")}</label><input type="text" id="rcEmail" placeholder="name@example.com" autocomplete="email"></div>
    <div class="login-err" id="rcErr"></div>
    <div class="mfa-foot"><button class="btn primary" id="rcAction" type="button">${I18N.t("mfa.send_code_btn")}</button></div>`;
  $("recoverMask").classList.add("show");

  $("rcAction").onclick = async () => {
    const errEl = $("rcErr"); errEl.textContent = "";
    const email = $("rcEmail").value.trim();
    if (!email) { errEl.textContent = I18N.t("valid.enter_email"); return; }
    try {
      const r = await fetch(`${API}/account/recover-send-code`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, purpose })
      });
      const j = await r.json().catch(() => ({}));
      if (r.ok) {
        toast(j.message || I18N.t("toast.code_sent"), "ok");
        showRecoverStep2(purpose, email);
      } else {
        errEl.textContent = j.error || I18N.t("toast.send_failed");
      }
    } catch (e) { errEl.textContent = I18N.t("toast.send_failed2") + e; }
  };
  setTimeout(() => { const el = $("rcEmail"); if (el) el.focus(); }, 60);
}

function showRecoverStep2(purpose, email) {
  const body = $("recoverBody");
  body.innerHTML = `
    <div class="mfa-desc" style="margin-bottom:14px">${I18N.t("recover.enter_code_desc")}</div>
    <div class="field" style="margin-bottom:8px"><label style="font-size:11px;color:var(--muted2)">${I18N.t("form.email")}：${esc(email)}</label></div>
    <div class="field"><label>${I18N.t("form.email_code")}</label><input type="text" id="rcCode" inputmode="numeric" maxlength="6" placeholder="${I18N.t('mfa.code_6')}" autocomplete="one-time-code"></div>
    <div class="login-err" id="rcErr"></div>
    <div class="mfa-foot" style="justify-content:space-between">
      <button class="btn" id="rcResend" type="button">${I18N.t("recover.resend_code")}</button>
      <button class="btn primary" id="rcAction" type="button">${I18N.t("recover.verify_code_btn")}</button>
    </div>`;

  $("rcResend").onclick = () => showRecoverFlow(purpose);
  $("rcAction").onclick = async () => {
    const errEl = $("rcErr"); errEl.textContent = "";
    const code = $("rcCode").value.trim();
    if (code.length !== 6) { errEl.textContent = I18N.t("valid.enter_code"); return; }
    try {
      const r = await fetch(`${API}/account/recover-verify`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, code, purpose })
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) { errEl.textContent = j.error || I18N.t("toast.verify_failed"); return; }
      if (j.mfa_required) {
        showRecoverStepMFA(purpose, email, code);
      } else {
        showRecoverResult(purpose, j);
      }
    } catch (e) { errEl.textContent = I18N.t("toast.send_failed2") + e; }
  };
  setTimeout(() => { const el = $("rcCode"); if (el) el.focus(); }, 60);
}

function showRecoverStepMFA(purpose, email, code) {
  const body = $("recoverBody");
  body.innerHTML = `
    <div class="mfa-desc" style="margin-bottom:14px">${I18N.t("recover.enter_totp_desc")}</div>
    <div class="field"><label>${I18N.t("recover.totp_code")}</label><input type="text" id="rcTOTP" inputmode="numeric" maxlength="8" placeholder="${I18N.t('recover.totp_placeholder')}" autocomplete="one-time-code"></div>
    <div class="login-err" id="rcErr"></div>
    <div class="mfa-foot" style="justify-content:space-between">
      <button class="btn" id="rcBack" type="button">${I18N.t("ui.back")}</button>
      <button class="btn primary" id="rcAction" type="button">${I18N.t("recover.verify_totp_btn")}</button>
    </div>`;

  $("rcBack").onclick = () => showRecoverStep2(purpose, email);
  $("rcAction").onclick = async () => {
    const errEl = $("rcErr"); errEl.textContent = "";
    const totpEl = $("rcTOTP");
    const totp = String(totpEl && totpEl.value || "").replace(/\D/g, "").slice(0, 6);
    if (totpEl) totpEl.value = totp;
    if (totp.length !== 6) { errEl.textContent = I18N.t("valid.enter_totp"); return; }
    try {
      const r = await fetch(`${API}/account/recover-verify-mfa`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, code, totp_code: totp, purpose })
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) {
        if (j.code === "totp_replay" || j.code === "totp_invalid") {
          if (totpEl) { totpEl.value = ""; totpEl.focus(); }
        }
        errEl.textContent = j.error || I18N.t("toast.verify_failed");
        return;
      }
      showRecoverResult(purpose, j);
    } catch (e) { errEl.textContent = I18N.t("toast.send_failed2") + e; }
  };
  setTimeout(() => { const el = $("rcTOTP"); if (el) el.focus(); }, 60);
}

function showRecoverResult(purpose, result) {
  const body = $("recoverBody");
  if (purpose === 'recover_username') {
    toast(I18N.t("toast.username_recovered"), "ok");
    body.innerHTML = `
      <div class="mfa-desc" style="margin-bottom:14px">${I18N.t("recover.username_recovered")}</div>
      <div class="field"><input type="text" value="${esc(result.username)}" readonly style="font-weight:700;font-size:16px;text-align:center;cursor:pointer" data-act="copy-input" title="${I18N.t('toast.copied')}"></div>
      <div class="mfa-foot"><button class="btn primary" id="rcClose" type="button">${I18N.t("recover.back_to_login")}</button></div>`;
    $("rcClose").onclick = () => $("recoverMask").classList.remove("show");
  } else {
    showSetNewPassword(result.reset_token);
  }
}

function showSetNewPassword(token) {
  const body = $("recoverBody");
  body.innerHTML = `
    <div class="mfa-desc" style="margin-bottom:14px">${I18N.t("recover.enter_new_password")}</div>
    <div class="field"><label>${I18N.t("form.new_password_min4")}</label><input type="password" id="rcNewPass" placeholder="${I18N.t('form.new_password')}"></div>
    <div class="field"><label>${I18N.t('profile.confirm_password') || I18N.t('form.new_password')}</label><input type="password" id="rcNewPass2" placeholder="${I18N.t('form.new_password')}"></div>
    <div class="login-err" id="rcErr"></div>
    <div class="mfa-foot"><button class="btn danger" id="rcReset" type="button">${I18N.t("recover.reset_password_btn")}</button></div>`;

  $("rcReset").onclick = async () => {
    const errEl = $("rcErr"); errEl.textContent = "";
    const p1 = $("rcNewPass").value;
    const p2 = $("rcNewPass2").value;
    if (!pwPolicyOK(p1)) { errEl.textContent = I18N.t("auth.password_policy"); return; }
    if (p1 !== p2) { errEl.textContent = I18N.t("auth.password_mismatch"); return; }
    try {
      const r = await fetch(`${API}/account/reset-password`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ reset_token: token, new_password: p1 })
      });
      const j = await r.json().catch(() => ({}));
      if (r.ok) {
        body.innerHTML = `
          <div class="mfa-desc" style="margin-bottom:14px;color:var(--ok);font-weight:600">✓ ${j.message || I18N.t("toast.password_reset2")}</div>
          <div class="mfa-foot"><button class="btn primary" id="rcClose" type="button">${I18N.t("recover.back_to_login")}</button></div>`;
        $("rcClose").onclick = () => $("recoverMask").classList.remove("show");
        toast(j.message || I18N.t("toast.password_reset2"), "ok");
      } else {
        errEl.textContent = j.error || I18N.t("toast.reset_failed");
      }
    } catch (e) { errEl.textContent = I18N.t("toast.reset_failed2") + e; }
  };
  setTimeout(() => { const el = $("rcNewPass"); if (el) el.focus(); }, 60);
}

async function logout() {
  try {
    await fetch(`${API}/logout`, { method: "POST", credentials: "same-origin" });
  } catch (e) { /* network / CSRF — still clear local session */ }
  try {
    // Best-effort cookie clear if API was blocked (e.g. transient CSRF behind proxy).
    document.cookie = "aiops_session=; Path=/; Max-Age=0; SameSite=Lax";
    document.cookie = "session=; Path=/; Max-Age=0; SameSite=Lax";
  } catch (_) {}
  location.href = "/";
}

/* ---------- 管理员：数据保留 / 命令策略 / PG 备份 ---------- */
async function loadOpsAdmin() {
  if (!isAdmin()) return;
  try {
    const [ret, pol, bak] = await Promise.all([
      fetch(`${API}/admin/retention`).then(r => r.json()),
      fetch(`${API}/admin/cmd-policy`).then(r => r.json()),
      fetch(`${API}/admin/backup-config`).then(r => r.json())
    ]);
    if ($("retAuditDays")) $("retAuditDays").value = ret.audit_days || 180;
    if ($("retAlertDays")) $("retAlertDays").value = ret.alert_history_days || 90;
    if ($("retContentDays")) $("retContentDays").value = ret.content_audit_days || 30;
    if ($("retAICallDays")) $("retAICallDays").value = ret.ai_call_days || 365;
    if ($("retNetflowMonths")) $("retNetflowMonths").value = ret.netflow_months || 12;
    if ($("retOpsHistoryDays")) $("retOpsHistoryDays").value = ret.ops_history_days || 90;
    if ($("cmdPolMode")) $("cmdPolMode").value = pol.mode || "strict";
    if ($("cmdPolAllow")) $("cmdPolAllow").value = (pol.allow_prefixes || []).join(",");
    if ($("cmdPolDeny")) $("cmdPolDeny").value = (pol.deny_patterns || []).join("\n");
    if ($("bakEnabled")) $("bakEnabled").checked = !!bak.enabled;
    if ($("bakDailyAt")) $("bakDailyAt").value = bak.daily_at || "02:30";
    if ($("bakRetain")) $("bakRetain").value = bak.retain_count || 14;
    if ($("bakDir")) $("bakDir").value = bak.dir || "";
    const rem = bak.remote || {};
    if ($("bakRemoteEnabled")) $("bakRemoteEnabled").checked = !!rem.enabled;
    if ($("bakRemoteEndpoint")) $("bakRemoteEndpoint").value = rem.endpoint || "";
    if ($("bakRemoteBucket")) $("bakRemoteBucket").value = rem.bucket || "";
    if ($("bakRemoteRegion")) $("bakRemoteRegion").value = rem.region || "";
    if ($("bakRemoteAccessKey")) $("bakRemoteAccessKey").value = rem.access_key || "";
    if ($("bakRemoteSecretKey")) $("bakRemoteSecretKey").value = "";
    if ($("bakRemotePrefix")) $("bakRemotePrefix").value = rem.prefix || "";
  } catch (e) { /* non-admin or API missing */ }
  await loadBackupList();
  await loadStatusPageCfg();
  await loadTicketSlaCfg();
  await loadSecretRotateStatus();
}
async function loadBackupList() {
  const el = $("backupList"); if (!el) return;
  try {
    const list = await fetch(`${API}/admin/backups`).then(r => r.json());
    if (!list || !list.length) { el.innerHTML = `<div class="empty-line">暂无备份文件</div>`; return; }
    el.innerHTML = list.map(b => {
      const mb = ((b.size_bytes || 0) / 1048576).toFixed(2);
      return `<div class="sre-row"><div class="sre-row-main"><div class="sre-row-title mono">${esc(b.id)}</div>
        <div class="sre-row-sub">${fmtDateTime(b.created_at)} · ${mb} MB · ${esc(b.operator || "")}${b.sha256 ? " · " + esc(b.sha256.slice(0, 12)) + "…" : ""}</div></div>
        <div class="user-acts">
          <a class="btn sm" href="${API}/admin/backups/${encodeURIComponent(b.id)}/download">下载</a>
          <button class="btn danger sm" data-bak="restore" data-id="${esc(b.id)}">还原</button>
        </div></div>`;
    }).join("");
    el.querySelectorAll("[data-bak=restore]").forEach(btn => btn.onclick = () => restoreBackup(btn.dataset.id));
  } catch (e) { el.innerHTML = `<div class="empty-line">加载失败（需管理员）</div>`; }
}
async function saveRetentionCfg() {
  const body = {
    audit_days: parseInt($("retAuditDays").value, 10) || 180,
    alert_history_days: parseInt($("retAlertDays").value, 10) || 90,
    content_audit_days: parseInt($("retContentDays").value, 10) || 30,
    ai_call_days: parseInt(($("retAICallDays") && $("retAICallDays").value) || "365", 10) || 365,
    netflow_months: parseInt($("retNetflowMonths").value, 10) || 12,
    // 必须一并回传：SetRetention 是整体覆盖，漏字段会把管理员设过的值悄悄重置成默认。
    ops_history_days: parseInt(($("retOpsHistoryDays") && $("retOpsHistoryDays").value) || "90", 10) || 90
  };
  const r = await fetch(`${API}/admin/retention`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (r.ok) toast("保留期已保存", "ok"); else toast(j.error || "保存失败", "err");
}
async function saveCmdPolicyCfg() {
  const body = {
    mode: $("cmdPolMode").value || "strict",
    allow_prefixes: ($("cmdPolAllow").value || "").split(",").map(s => s.trim()).filter(Boolean),
    deny_patterns: ($("cmdPolDeny").value || "").split("\n").map(s => s.trim()).filter(Boolean)
  };
  const r = await fetch(`${API}/admin/cmd-policy`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (r.ok) toast("命令策略已保存", "ok"); else toast(j.error || "保存失败", "err");
}
async function saveBackupCfg() {
  const body = {
    enabled: $("bakEnabled").checked,
    daily_at: ($("bakDailyAt").value || "02:30").trim(),
    retain_count: parseInt($("bakRetain").value, 10) || 14,
    dir: ($("bakDir").value || "").trim(),
    remote: {
      enabled: !!( $("bakRemoteEnabled") && $("bakRemoteEnabled").checked ),
      endpoint: ($("bakRemoteEndpoint") && $("bakRemoteEndpoint").value || "").trim(),
      bucket: ($("bakRemoteBucket") && $("bakRemoteBucket").value || "").trim(),
      region: ($("bakRemoteRegion") && $("bakRemoteRegion").value || "").trim(),
      access_key: ($("bakRemoteAccessKey") && $("bakRemoteAccessKey").value || "").trim(),
      secret_key: ($("bakRemoteSecretKey") && $("bakRemoteSecretKey").value || "").trim(),
      prefix: ($("bakRemotePrefix") && $("bakRemotePrefix").value || "").trim()
    }
  };
  const r = await fetch(`${API}/admin/backup-config`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (r.ok) toast("备份计划已保存", "ok"); else toast(j.error || "保存失败", "err");
}
async function loadStatusPageCfg() {
  try {
    const c = await fetch(`${API}/admin/status-page`).then(r => r.json());
    if ($("statusPageEnabled")) $("statusPageEnabled").checked = !!c.enabled;
    if ($("statusPageTitle")) $("statusPageTitle").value = c.title || "";
    if ($("statusPageSubtitle")) $("statusPageSubtitle").value = c.subtitle || "";
    if ($("statusPageToken")) $("statusPageToken").value = c.public_token || "";
  } catch (e) {}
}
async function saveStatusPageCfg() {
  const body = {
    enabled: !!( $("statusPageEnabled") && $("statusPageEnabled").checked ),
    title: ($("statusPageTitle") && $("statusPageTitle").value || "").trim(),
    subtitle: ($("statusPageSubtitle") && $("statusPageSubtitle").value || "").trim(),
    public_token: ($("statusPageToken") && $("statusPageToken").value || "").trim()
  };
  const r = await fetch(`${API}/admin/status-page`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (r.ok) toast("Status Page 已保存", "ok"); else toast(j.error || "保存失败", "err");
}
function parseSlaPair(el, dResp, dResolve) {
  const raw = ((el && el.value) || "").trim();
  if (!raw) return [dResp, dResolve];
  const parts = raw.split(/[/|,]/).map(s => parseInt(s.trim(), 10)).filter(n => n > 0);
  return [parts[0] || dResp, parts[1] || dResolve];
}
async function loadTicketSlaCfg() {
  try {
    const p = await fetch(`${API}/tickets/sla`).then(r => r.json());
    if ($("ticketSlaAutoAssign")) $("ticketSlaAutoAssign").checked = p.auto_assign !== false;
    const rm = p.response_min || {}, sm = p.resolve_min || {};
    if ($("ticketSlaP1")) $("ticketSlaP1").value = `${rm.p1 || 15}/${sm.p1 || 240}`;
    if ($("ticketSlaP2")) $("ticketSlaP2").value = `${rm.p2 || 60}/${sm.p2 || 1440}`;
    if ($("ticketSlaP3")) $("ticketSlaP3").value = `${rm.p3 || 240}/${sm.p3 || 4320}`;
  } catch (e) {}
}
async function saveTicketSlaCfg() {
  const [p1r, p1s] = parseSlaPair($("ticketSlaP1"), 15, 240);
  const [p2r, p2s] = parseSlaPair($("ticketSlaP2"), 60, 1440);
  const [p3r, p3s] = parseSlaPair($("ticketSlaP3"), 240, 4320);
  const body = {
    auto_assign: !!( $("ticketSlaAutoAssign") && $("ticketSlaAutoAssign").checked ),
    response_min: { p1: p1r, p2: p2r, p3: p3r, p4: 1440 },
    resolve_min: { p1: p1s, p2: p2s, p3: p3s, p4: 10080 }
  };
  const r = await fetch(`${API}/tickets/sla`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (r.ok) toast("工单 SLA 已保存", "ok"); else toast(j.error || "保存失败", "err");
}
async function showTicketSlaBreaches() {
  const el = $("ticketSlaBreachList"); if (!el) return;
  try {
    const j = await fetch(`${API}/tickets/sla/breaches`).then(r => r.json());
    const list = j.breaches || [];
    if (!list.length) { el.textContent = "当前无 SLA 违约工单"; return; }
    el.innerHTML = list.slice(0, 20).map(b =>
      `#${b.ticket_id} ${esc(b.title || "")} · ${esc(b.breach)} · ${b.age_min || 0}min`
    ).join("<br>");
  } catch (e) { el.textContent = "加载失败"; }
}
async function loadSecretRotateStatus() {
  const el = $("secretRotateStatus"); if (!el) return;
  try {
    const j = await fetch(`${API}/security/secret-rotate`).then(r => r.json());
    const ids = (j.key_ids || []).join(", ");
    el.textContent = `主密钥 ${j.primary_id || "-"} · 可用 ${ids || "无"} · 间隔 ${j.interval_days || 0} 天 · 库 ${j.store_loaded ? "已加载" : "未加载"}`;
  } catch (e) { el.textContent = "无法读取密钥状态（需管理员）"; }
}
async function rotateSecretKeyNow() {
  const conf = await requestAITextInput({
    title: "确认轮换配置加密密钥",
    message: "将生成新主密钥并重加密配置。请输入 ROTATE 确认：",
    label: "确认文本", placeholder: "ROTATE", submitLabel: "确认轮换",
    singleLine: true, maxLength: 32, danger: true, requiredMessage: "请输入 ROTATE"
  });
  if (!conf) return;
  const r = await fetch(`${API}/security/secret-rotate`, {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ confirm: conf, interval_days: 90 })
  });
  const j = await r.json().catch(() => ({}));
  if (r.ok) { toast("密钥已轮换：" + (j.primary_id || ""), "ok"); await loadSecretRotateStatus(); }
  else toast(j.error || "轮换失败", "err");
}
async function createBackupNow() {
  await withLoading("bakNowBtn", async () => {
    const r = await fetch(`${API}/admin/backups`, { method: "POST" });
    const j = await r.json().catch(() => ({}));
    if (r.ok) { toast("备份完成：" + (j.id || ""), "ok"); await loadBackupList(); }
    else toast(j.error || "备份失败（请确认 pg_dump 在 PATH）", "err");
  });
}
async function restoreBackup(id) {
  const conf = await requestAITextInput({
    title:"确认还原数据库",message:`还原会覆盖当前 PostgreSQL 数据。请输入 RESTORE 或备份 ID 确认：${id}`,
    label:"确认文本",placeholder:"RESTORE",submitLabel:"确认还原",
    singleLine:true,maxLength:256,danger:true,requiredMessage:"请输入 RESTORE 或备份 ID"
  });
  if (!conf) return;
  const r = await fetch(`${API}/admin/backups/${encodeURIComponent(id)}/restore`, {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ confirm: conf })
  });
  const j = await r.json().catch(() => ({}));
  if (r.ok) toast(j.hint || "还原完成，请重启服务端", "ok");
  else toast(j.error || "还原失败", "err");
}
safeAddEventListener("retSaveBtn", "click", saveRetentionCfg);
safeAddEventListener("cmdPolSaveBtn", "click", saveCmdPolicyCfg);
safeAddEventListener("bakCfgSaveBtn", "click", saveBackupCfg);
safeAddEventListener("bakNowBtn", "click", createBackupNow);
safeAddEventListener("statusPageSaveBtn", "click", saveStatusPageCfg);
safeAddEventListener("ticketSlaSaveBtn", "click", saveTicketSlaCfg);
safeAddEventListener("ticketSlaBreachBtn", "click", showTicketSlaBreaches);
safeAddEventListener("secretRotateRefreshBtn", "click", loadSecretRotateStatus);
safeAddEventListener("secretRotateBtn", "click", rotateSecretKeyNow);
safeAddEventListener("webhookPresetSlack", "click", () => {
  const ct = $("customWebhookContentType");
  const body = $("customWebhookBodyTemplate");
  if (ct) ct.value = "application/json";
  if (body) body.value = '{"text":"{{.Text}}"}';
  toast(I18N.t("settings.webhook_presets", "快速模板") + ": Slack", "ok");
});
safeAddEventListener("webhookPresetTeams", "click", () => {
  const ct = $("customWebhookContentType");
  const body = $("customWebhookBodyTemplate");
  if (ct) ct.value = "application/json";
  if (body) body.value = '{"@type":"MessageCard","@context":"https://schema.org/extensions","summary":"AIOps","title":"[{{.Level}}] {{.Hostname}}","text":"{{.Text}}"}';
  toast(I18N.t("settings.webhook_presets", "快速模板") + ": Microsoft Teams", "ok");
});
