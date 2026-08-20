/* ---------- 远程终端（经 Agent 反向通道）· 多标签 ---------- */
let TERM_TABS = [];      // [{id, name, ws, vt, screenEl, tabEl, retry}]
let TERM_ACTIVE = -1;    // active tab index
let TERM_RESIZE = null;  // window resize listener
let TERM_SCREEN_RO = null;   // ResizeObserver on the active screen box
let TERM_RO_TARGET = null;   // element TERM_SCREEN_RO is currently watching
let TERM_RO_PENDING = 0;     // rAF handle coalescing observer callbacks
let TERM_VP_BOUND = false;   // visualViewport listeners attached once, not per tab

/* ---------- v5.4.0: Web Worker 心跳（绕过浏览器后台 Tab 节流）---------- */
// 现代浏览器对后台 Tab 的 setTimeout/setInterval 节流到最低 1 分钟，
// 导致服务端 25s ping 的 pong 可能延迟 → 代理/NAT 认为连接空闲超时。
// Web Worker 不受此限制，每 15s 发送一个 keepalive 帧保持连接活跃。
let _termHeartbeatWorker = null;
function ensureTermHeartbeatWorker() {
  if (_termHeartbeatWorker) return;
  try {
    const blob = new Blob([`
      let timer = null;
      self.onmessage = function(e) {
        if (e.data === "start") {
          if (timer) clearInterval(timer);
          timer = setInterval(function() { self.postMessage("tick"); }, 15000);
        } else if (e.data === "stop") {
          if (timer) { clearInterval(timer); timer = null; }
        }
      };
    `], { type: "application/javascript" });
    _termHeartbeatWorker = new Worker(URL.createObjectURL(blob));
    _termHeartbeatWorker.onmessage = () => {
      // Worker tick: send keepalive to all connected terminal tabs
      for (const tab of TERM_TABS) {
        if (tab.ws && tab.ws.readyState === 1) {
          try {
            // Send a single 'i' byte (0x69) with NO payload — server skips
            // empty payloads (see handleTerminal: len(payload)==0 → continue)
            // so this only keeps the TCP connection alive without affecting PTY.
            tab.ws.send(new Uint8Array([0x69]));
          } catch (_) {}
        }
      }
    };
    _termHeartbeatWorker.postMessage("start");
  } catch (_) {
    // Worker creation failed — fallback: use setInterval (will be throttled in background)
    setInterval(() => {
      for (const tab of TERM_TABS) {
        if (tab.ws && tab.ws.readyState === 1) {
          try { tab.ws.send(new Uint8Array([0x69])); } catch (_) {}
        }
      }
    }, 15000);
  }
}

// 从其他应用切换回浏览器窗口时恢复终端焦点
window.addEventListener("focus", () => {
  // 延迟 50ms 等浏览器完成焦点恢复流程
  setTimeout(_refocusActiveTermInput, 50);
});

/* ---------- v5.4.0: 页面可见性变化 — Tab 恢复时立即检查并重连 ---------- */
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState !== "visible") return;
  for (const tab of TERM_TABS) {
    if (!tab.ws || tab.ws.readyState === 3 /* CLOSED */ || tab.ws.readyState === 2 /* CLOSING */) {
      // Auto-reconnect when tab becomes visible and WS is dead
      if (tab._autoReconnecting) continue;
      tab._autoReconnecting = true;
      tab.retry = 0;
      setTimeout(() => {
        tab._autoReconnecting = false;
        if (!tab.ws || tab.ws.readyState !== 1) connectTermWS(tab);
      }, 500);
    }
  }
  // 切回标签页时立即恢复活动终端的输入焦点
  _refocusActiveTermInput();
});

/* ---------- 全局焦点守卫 — 防止终端输入焦点在各种操作中丢失 ---------- */
// 当终端面板可见且焦点漂移到非终端元素时，自动恢复焦点。
// 覆盖场景：窗口 resize 后、最大化/还原后、从 dock 展开后、WS 重连后、
// 浏览器标签页切换回来后、用户点击终端区域但焦点未进入 textarea 等。
//
// 但它必须给"选文本"让路：focus() 会把选区上下文切到 textarea，document 选区当场清空。
// 用户按下鼠标开始拖选时，<pre> 会先拿到焦点，守卫如果这时把焦点抢回 textarea，
// 拖拽就永远选不出东西——这正是"Linux 终端里复制不了命令和窗口内容"的最后一条根因。
function termFocusGuardBusy() {
  for (const tab of TERM_TABS) {
    if (tab && tab.screenEl && tab.screenEl._termDragging) return true;   // 正在拖选
  }
  const sel = window.getSelection();
  if (!sel || sel.rangeCount === 0) return false;
  const text = sel.toString();
  if (!text) return false;
  const tab = TERM_ACTIVE >= 0 ? TERM_TABS[TERM_ACTIVE] : null;
  if (!tab || !tab.screenEl) return false;
  for (let i = 0; i < sel.rangeCount; i++) {
    if (tab.screenEl.contains(sel.getRangeAt(i).commonAncestorContainer)) return true; // 选区还在，别动焦点
  }
  return false;
}

function _refocusActiveTermInput() {
  if (termFocusGuardBusy()) return;
  if (TERM_ACTIVE < 0 || !TERM_TABS[TERM_ACTIVE]) return;
  const mask = $("termMask");
  if (!mask || !mask.classList.contains("show")) return; // 终端面板不可见
  const tab = TERM_TABS[TERM_ACTIVE];
  if (!tab.inputEl) return;
  // 如果焦点已在 textarea 上，无需操作
  if (document.activeElement === tab.inputEl) return;
  // 如果焦点在终端区域内的其他元素（如 screen pre），重定向到 textarea
  if (tab.screenEl && tab.screenEl.contains(document.activeElement)) {
    tab.inputEl.focus({ preventScroll: true });
    return;
  }
  // 焦点完全不在终端区域 — 恢复焦点
  tab.inputEl.focus({ preventScroll: true });
}
document.addEventListener("focusin", () => {
  // 焦点变化时检查：如果终端可见但焦点不在终端内，延迟恢复
  // 使用 setTimeout 避免与正在进行的 click/keydown 事件冲突
  if (TERM_ACTIVE < 0 || !TERM_TABS[TERM_ACTIVE]) return;
  const mask = $("termMask");
  if (!mask || !mask.classList.contains("show")) return;
  const tab = TERM_TABS[TERM_ACTIVE];
  if (!tab.inputEl) return;
  // 如果焦点在终端区域外（如导航栏、弹窗等），不强制拉回
  if (document.activeElement && !tab.screenEl.contains(document.activeElement)
      && document.activeElement !== tab.inputEl) return;
  // 焦点在 screen 但不在 textarea — 重定向（拖选期间不动，否则选区当场没）
  if (document.activeElement === tab.screenEl) {
    if (termFocusGuardBusy()) return;
    setTimeout(() => {
      if (termFocusGuardBusy()) return;
      if (document.activeElement === tab.screenEl) tab.inputEl.focus({ preventScroll: true });
    }, 0);
  }
});

/* ---------- v5.3.0: 终端二次认证 ---------- */
let TERM_AUTH_VERIFIED = false;    // 当前会话是否已验证终端密码
let TERM_AUTH_CHECKING = false;    // 是否正在执行认证流程
let TERM_AUTH_PENDING = null;      // 待处理的终端打开请求 {id, name}

function openTerminal(id, name, opts) {
  opts = opts || {};
  // 多会话支持：同一 hostID 可创建多个标签页，每个标签页拥有独立的 WebSocket 连接。
  // 容器终端每次新建（不同 containerId）；宿主机终端可从 dock 恢复。
  if (!opts.containerId) {
    const dockedIdx = TERM_TABS.findIndex(t => t.id === id && !t.containerId && TERM_DOCK_IDS.has(t.id));
    if (dockedIdx >= 0) {
      TERM_DOCK_IDS.delete(id);
      const dockItem = $("termDock") && $("termDock").querySelector(`[data-tab-id="${CSS.escape(id)}"]`);
      if (dockItem) dockItem.remove();
      updateTermDock();
      switchTermTab(dockedIdx);
      $("termMask").classList.add("show");
      requestAnimationFrame(() => requestAnimationFrame(termRefit));
      return;
    }
  }

  // v5.3.0: 终端二次认证流程
  if (TERM_AUTH_CHECKING) return; // 避免重复触发
  TERM_AUTH_PENDING = {
    id, name,
    containerId: opts.containerId || "",
    containerName: opts.containerName || "",
    shell: opts.shell || "sh",
  };
  checkTerminalAccess();
}

// Win2012 / Win8 pipe shells have no ConPTY echo — paint keystrokes locally.
function hostNeedsTermLocalEcho(hostId) {
  try {
    const list = (typeof LAST_HOSTS !== "undefined" && Array.isArray(LAST_HOSTS)) ? LAST_HOSTS
      : (Array.isArray(window._cachedHosts) ? window._cachedHosts : []);
    const h = list.find(x => x && (x.id === hostId || x.host_id === hostId));
    if (!h) return false;
    const os = String(h.os || "").toLowerCase();
    if (os && os !== "windows") return false;
    const p = String(h.platform || "").toLowerCase();
    return /server 2012|server 2008|windows 8|windows 8\.1|build 9200|build 9600|build 7601|build 600/.test(p);
  } catch (e) {
    return false;
  }
}

function termLocalEcho(tab, str) {
  if (!tab || !tab.localEcho || !tab.vt || !str) return;
  let out = "";
  for (let i = 0; i < str.length; i++) {
    const c = str.charCodeAt(i);
    if (c === 13 || c === 10) out += "\r\n";
    else if (c === 8 || c === 127) out += "\b \b";
    else if (c === 3) out += "^C";
    else if (c >= 32 || c === 9) out += str.charAt(i);
  }
  if (out) tab.vt.feed(out);
}

window.openContainerTerminal = function (hostId, hostName, containerId, containerName, shell) {
  openTerminal(hostId, hostName || hostId, {
    containerId, containerName: containerName || containerId, shell: shell || "sh",
  });
};

/* ---------- 终端右键菜单 ---------- */
let TERM_CMENU_EL = null;
function initTermContextMenu() {
  if (TERM_CMENU_EL) return;
  TERM_CMENU_EL = document.createElement("div");
  TERM_CMENU_EL.className = "term-cmenu";
  TERM_CMENU_EL.innerHTML = `
    <div class="term-cmenu-item" data-action="copy">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
      <span>${esc(I18N.t("term.cmenu_copy", "复制"))}</span><span class="cmenu-key">Ctrl+C</span>
    </div>
    <div class="term-cmenu-item" data-action="paste">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/><rect x="8" y="2" width="8" height="4" rx="1" ry="1"/></svg>
      <span>${esc(I18N.t("term.cmenu_paste", "粘贴"))}</span><span class="cmenu-key">Ctrl+V</span>
    </div>
    <div class="term-cmenu-sep"></div>
    <div class="term-cmenu-item" data-action="select-all">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="2"/><path d="M8 12h8M12 8v8"/></svg>
      <span>${esc(I18N.t("term.cmenu_select_all", "全选"))}</span>
    </div>
    <div class="term-cmenu-item" data-action="copy-all">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 4h10v4"/><rect x="8" y="8" width="12" height="12" rx="2"/><path d="M4 4v10h4"/></svg>
      <span>${esc(I18N.t("term.cmenu_copy_all", "复制全部内容"))}</span>
    </div>
    <div class="term-cmenu-sep"></div>
    <div class="term-cmenu-item" data-action="reconnect">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.5 2v6h-6M2.5 22v-6h6M2 11.5a10 10 0 0 1 18.8-4.3M22 12.5a10 10 0 0 1-18.8 4.2"/></svg>
      <span>${esc(I18N.t("term.cmenu_reconnect", "重新连接"))}</span>
    </div>
    <div class="term-cmenu-sep"></div>
    <div class="term-cmenu-item" data-action="clear">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z"/></svg>
      <span>${esc(I18N.t("term.cmenu_clear", "清屏"))}</span>
    </div>
  `;
  document.body.appendChild(TERM_CMENU_EL);
  // 点击菜单外部关闭
  document.addEventListener("click", (e) => {
    if (TERM_CMENU_EL && !TERM_CMENU_EL.contains(e.target)) {
      TERM_CMENU_EL.classList.remove("show");
    }
  });
  // Esc 关闭
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && TERM_CMENU_EL) {
      TERM_CMENU_EL.classList.remove("show");
    }
  });
  // 菜单项点击处理
  TERM_CMENU_EL.addEventListener("click", (e) => {
    const item = e.target.closest(".term-cmenu-item");
    if (!item || item.classList.contains("disabled")) return;
    const action = item.dataset.action;
    const tab = TERM_CMENU_EL._termTab;
    TERM_CMENU_EL.classList.remove("show");
    if (!tab) return;
    switch (action) {
      case "copy":
        // 用菜单弹出那一刻抓到的选区：读选区在极端情况下要 blur 隐藏 textarea 再读，
        // 而把焦点还回去会清掉高亮——点菜单时再读就成了空的。
        termCopyText(TERM_CMENU_EL._termSel || termSelectionText(tab));
        break;
      case "select-all":
        termSelectAllScreen(tab);
        break;
      case "copy-all":
        termCopyText(termScreenText(tab));
        break;
      case "paste": {
        // 聚焦输入框：读剪贴板失败时（明文 HTTP 下 navigator.clipboard 根本不存在，
        // 或用户拒绝了读取授权）用户还能自己按 Ctrl+V，走原生 paste 事件那条路。
        if (tab.inputEl) tab.inputEl.focus({ preventScroll: true });
        if (navigator.clipboard && navigator.clipboard.readText) {
          navigator.clipboard.readText().then(t => {
            if (t && tab.ws && tab.ws.readyState === 1) termSend(tab.ws, t);
          }).catch(() => toast(I18N.t("term.paste_manual", "浏览器不允许读取剪贴板，请按 Ctrl+V 粘贴"), "info"));
        } else {
          toast(I18N.t("term.paste_manual", "浏览器不允许读取剪贴板，请按 Ctrl+V 粘贴"), "info");
        }
        break;
      }
      case "reconnect":
        if (tab.ws && tab.ws.readyState === 1) { toast(I18N.t("term.connected"), "info"); return; }
        reconnectTermTab(tab);
        break;
      case "clear":
        if (tab.vt && tab.vt.fullReset) {
          tab.vt.fullReset();
          tab.vt.render();
        }
        break;
    }
  });
}
function showTermContextMenu(tab, e) {
  initTermContextMenu();
  if (!TERM_CMENU_EL) return;
  e.preventDefault();
  e.stopPropagation();
  TERM_CMENU_EL._termTab = tab;
  // 更新菜单项状态
  const copyItem = TERM_CMENU_EL.querySelector('[data-action="copy"]');
  const reconnectItem = TERM_CMENU_EL.querySelector('[data-action="reconnect"]');
  const selText = termSelectionText(tab);
  TERM_CMENU_EL._termSel = selText;
  if (copyItem) copyItem.classList.toggle("disabled", !selText);
  const disconnected = !tab.ws || tab.ws.readyState !== 1;
  if (reconnectItem) reconnectItem.classList.toggle("disabled", !disconnected);
  // 定位
  TERM_CMENU_EL.style.display = "block";
  let x = e.clientX, y = e.clientY;
  const mw = TERM_CMENU_EL.offsetWidth || 160;
  const mh = TERM_CMENU_EL.offsetHeight || 150;
  if (x + mw > window.innerWidth) x = window.innerWidth - mw - 4;
  if (y + mh > window.innerHeight) y = window.innerHeight - mh - 4;
  if (x < 0) x = 4;
  if (y < 0) y = 4;
  TERM_CMENU_EL.style.left = x + "px";
  TERM_CMENU_EL.style.top = y + "px";
  TERM_CMENU_EL.classList.add("show");
}

/* ---------- v5.3.0: 终端二次认证流程 ---------- */
const TERM_PROTOCOL_KEY = "aiops_term_protocol_agreed";

// 远程闸门预检（统一文案徽章，终端/桌面/剧本共用）
function freezeBadgeHTML(pf){
  if(!pf||!pf.freeze_active) return "";
  const name=pf.freeze_window||"";
  return `<span class="badge freeze" title="${esc(pf.freeze_note||pf.gate_reason||"")}">冻结${name?" · "+esc(name):""}</span>`;
}
function remoteGateBadgeHTML(pf){
  if(!pf) return "";
  if(pf.gate_required) return `<span class="badge warn" title="${esc(pf.gate_reason||"")}">远程闸门</span>`;
  if(pf.freeze_active||pf.high_risk) return `<span class="badge ok" title="${esc(pf.gate_reason||"已放行")}">闸门已放行</span>`;
  return "";
}
async function fetchRemotePreflight(hostId){
  try{
    const r=await fetch(`${API}/hosts/${encodeURIComponent(hostId)}/remote-preflight`,{credentials:"include"});
    if(!r.ok) return null;
    return await r.json();
  }catch(e){ return null; }
}
/** 闸门拦截时提示；管理员可强制放行。返回 true=继续打开。 */
async function confirmRemoteGate(hostId, opts){
  opts=opts||{};
  const pf=await fetchRemotePreflight(hostId);
  if(!pf||!pf.gate_required){
    if(pf&&(pf.freeze_active||pf.high_risk)){
      // soft chip only — already allowed
    }
    return {ok:true, breakGlass:false, pf};
  }
  const tip=pf.gate_reason||(typeof I18N!=="undefined"
    ? I18N.t("remote.gate_default", "当前主机处于冻结或高危状态，需已批准变更或事件闭环批准后方可远程操控。")
    : "当前主机处于冻结或高危状态，需已批准变更或事件闭环批准后方可远程操控。");
  if(pf.break_glass_ok){
    const ok = typeof uiConfirm === "function"
      ? await uiConfirm({
          title: typeof I18N!=="undefined" ? I18N.t("remote.gate_title", "远程操作被闸门拦截") : "远程操作被闸门拦截",
          message: tip,
          detail: typeof I18N!=="undefined" ? I18N.t("remote.gate_admin_detail", "你是管理员：可强制打开并继续。此操作将写入审计日志。") : "你是管理员：可强制打开并继续。此操作将写入审计日志。",
          confirmText: typeof I18N!=="undefined" ? I18N.t("remote.gate_force_open", "强制打开") : "强制打开",
          cancelText: typeof I18N!=="undefined" ? I18N.t("ui.confirm_cancel", "取消") : "取消",
          tone: "warn"
        })
      : confirm(tip + "\n\n你是管理员：是否强制打开？（将记入审计）");
    if(ok) return {ok:true, breakGlass:true, pf};
    return {ok:false, breakGlass:false, pf};
  }
  toast(tip,"err");
  return {ok:false, breakGlass:false, pf};
}

// 实际执行终端打开（原 openTerminal 后半部分逻辑）
async function doOpenTerminal(id, name, opts) {
  opts = opts || {};
  const gate=await confirmRemoteGate(id,{container:!!opts.containerId});
  if(!gate.ok) return;
  opts.breakGlass=!!gate.breakGlass;
  const sameHostTabs = TERM_TABS.filter(t => t.hostId === id && !!t.containerId === !!opts.containerId);
  let tabName = name;
  if (opts.containerId) {
    tabName = (opts.containerName || opts.containerId.slice(0, 12)) + "@" + (name || id);
  } else if (sameHostTabs.length > 0) {
    tabName = `${name} (${sameHostTabs.length + 1})`;
  }
  createTermTab(id, name, tabName, opts);
}

// 终端访问权限检查：协议 → 密码状态 → 验证
async function checkTerminalAccess() {
  if (TERM_AUTH_CHECKING) return;
  TERM_AUTH_CHECKING = true;

  try {
    // 1. 检查协议是否已同意
    if (!localStorage.getItem(TERM_PROTOCOL_KEY)) {
      showTermProtocol();
      return;
    }

    // 2. 检查是否已设置密码
    const statusRes = await fetch("/api/user/terminal-password/status", { credentials: "include" });
    const status = await statusRes.json().catch(() => ({}));

    if (!status.has_password) {
      // 未设置密码，弹出设置窗口
      showTermSetPassword();
      return;
    }

    // 3. 检查当前会话是否已验证——以服务端会话状态为准：浏览器刷新后本地
    //    TERM_AUTH_VERIFIED 会被重置，但服务端 session 仍记得已验证，
    //    因此这里读取 status.verified 并同步本地标记，避免刷新后反复重输终端密码。
    if (status.verified) TERM_AUTH_VERIFIED = true;
    if (TERM_AUTH_VERIFIED) {
      proceedToTerminal();
      return;
    }

    // 需要验证密码
    showTermVerify();
  } catch (e) {
    TERM_AUTH_CHECKING = false;
    toast(I18N.t("toast.network_error"), "err");
  }
}

// 协议同意后继续流程
function onTermProtocolAgreed() {
  localStorage.setItem(TERM_PROTOCOL_KEY, "1");
  $("termProtocolMask").classList.remove("show");
  // 重置检查锁，让 checkTerminalAccess 能继续执行后续步骤
  TERM_AUTH_CHECKING = false;
  checkTerminalAccess();
}

// 显示协议弹窗
function showTermProtocol() {
  $("termProtocolAgree").checked = false;
  $("termProtocolContinue").disabled = true;
  $("termProtocolMask").classList.add("show");
}

// 显示密码设置弹窗
function showTermSetPassword() {
  $("termSetPwd").value = "";
  $("termSetPwd2").value = "";
  $("termSetPwdErr").textContent = "";
  $("termSetPwdErr").style.display = "none";
  $("termSetPwdMask").classList.add("show");
}

// 显示密码验证弹窗
function showTermVerify() {
  $("termVerifyPwd").value = "";
  $("termVerifyErr").textContent = "";
  $("termVerifyErr").style.display = "none";
  $("termAttemptsInfo").style.display = "none";
  $("termVerifyMask").classList.add("show");
  setTimeout(() => { const el = $("termVerifyPwd"); if (el) el.focus(); }, 100);
}

// 密码设置/验证完成后打开终端或桌面
function proceedToTerminal() {
  TERM_AUTH_CHECKING = false;
  if (!TERM_AUTH_PENDING) return;
  const pending = TERM_AUTH_PENDING;
  TERM_AUTH_PENDING = null;
  const { id, name, action } = pending;
  if (action === "desktop") doOpenDesktop(id, name);
  else doOpenTerminal(id, name, pending);
}

// 取消终端认证流程
function cancelTermAuth() {
  TERM_AUTH_CHECKING = false;
  TERM_AUTH_PENDING = null;
  $("termProtocolMask").classList.remove("show");
  $("termSetPwdMask").classList.remove("show");
  $("termVerifyMask").classList.remove("show");
}

// 提交设置终端密码
async function submitTermSetPassword() {
  const pwd = $("termSetPwd").value;
  const pwd2 = $("termSetPwd2").value;
  const errEl = $("termSetPwdErr");

  if (!pwd || !pwd2) {
    errEl.textContent = I18N.t("valid.fill_password");
    errEl.style.display = "block";
    return;
  }
  if (pwd !== pwd2) {
    errEl.textContent = I18N.t("term_auth.password_mismatch");
    errEl.style.display = "block";
    return;
  }
  if (pwd.length < 8) {
    errEl.textContent = I18N.t("term_auth.password_too_short");
    errEl.style.display = "block";
    return;
  }

  try {
    const r = await fetch("/api/user/terminal-password/set", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password: pwd })
    });
    const j = await r.json().catch(() => ({}));
    if (r.ok) {
      TERM_AUTH_VERIFIED = true;
      $("termSetPwdMask").classList.remove("show");
      toast(I18N.t("term_auth.password_set_ok"), "ok");
      proceedToTerminal();
    } else {
      errEl.textContent = j.error || I18N.t("toast.save_failed");
      errEl.style.display = "block";
    }
  } catch (e) {
    errEl.textContent = I18N.t("toast.network_error");
    errEl.style.display = "block";
  }
}

// 提交验证终端密码
async function submitTermVerify() {
  const pwd = $("termVerifyPwd").value;
  const errEl = $("termVerifyErr");
  const attemptsEl = $("termAttemptsInfo");

  if (!pwd) {
    errEl.textContent = I18N.t("valid.enter_password");
    errEl.style.display = "block";
    return;
  }

  try {
    const r = await fetch("/api/user/terminal-password/verify", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password: pwd })
    });
    const j = await r.json().catch(() => ({}));

    if (r.ok) {
      TERM_AUTH_VERIFIED = true;
      $("termVerifyMask").classList.remove("show");
      toast(I18N.t("term_auth.password_verified"), "ok");
      proceedToTerminal();
    } else {
      if (j.locked) {
        // 锁定状态
        $("termVerifyMask").classList.remove("show");
        $("termLockedMask").classList.add("show");
        TERM_AUTH_CHECKING = false;
        TERM_AUTH_PENDING = null;
        return;
      }
      errEl.textContent = j.error || I18N.t("toast.verify_failed");
      errEl.style.display = "block";
      if (typeof j.remaining === "number" && j.remaining > 0) {
        attemptsEl.textContent = I18N.t("term_auth.remaining_attempts") + j.remaining;
        attemptsEl.style.display = "block";
      }
      $("termVerifyPwd").value = "";
      $("termVerifyPwd").focus();
    }
  } catch (e) {
    errEl.textContent = I18N.t("toast.network_error");
    errEl.style.display = "block";
  }
}

// 密码可见性切换
function toggleTermPwdVisibility(inputId, btnId) {
  const input = $(inputId);
  const btn = $(btnId);
  if (!input || !btn) return;
  const isPassword = input.type === "password";
  input.type = isPassword ? "text" : "password";
  btn.querySelector("svg").innerHTML = isPassword
    ? '<path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/>'
    : '<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/>';
}

function createTermTab(id, name, tabName, opts) {
  opts = opts || {};
  tabName = tabName || name;
  const screens = $("termScreens"), tabbar = $("termTabbar");
  const screen = document.createElement("pre");
  screen.className = "term-screen"; screen.tabIndex = 0; screen.spellcheck = false;
  screens.appendChild(screen);
  const tab = document.createElement("button");
  tab.className = "term-tab";
  tab.innerHTML = `<span>${esc(tabName)}</span><span class="term-tab-close" title="${I18N.t('ui.close_tab')}">×</span>`;
  tabbar.appendChild(tab);
  const vt = makeVT(screen);
  screen._vt = vt;
  /* 移动端虚拟键盘支持：在终端屏幕内注入隐藏 textarea 捕获输入。
     必须在 makeVT() 之后创建——makeVT 会 screen.innerHTML="" 清空子节点。
     <pre> 元素在移动端无法唤起虚拟键盘，<textarea> 可以。 */
  const input = document.createElement("textarea");
  input.className = "term-input";
  input.setAttribute("autocapitalize", "off");
  input.setAttribute("autocorrect", "off");
  input.setAttribute("autocomplete", "off");
  input.setAttribute("spellcheck", "false");
  input.setAttribute("aria-label", I18N.t("misc.terminal_input"));
  input.setAttribute("enterkeyhint", "enter");
  input.setAttribute("rows", "1");
  input.setAttribute("wrap", "off");
  input.readOnly = false;
  screen.appendChild(input);
  const tabObj = {
    id, hostId: id, name, tabName, ws: null, vt, screenEl: screen, tabEl: tab, inputEl: input, retry: 0, composing: false,
    containerId: opts.containerId || "",
    containerName: opts.containerName || "",
    shell: opts.shell || "sh",
    breakGlass: !!opts.breakGlass,
    localEcho: !opts.containerId && hostNeedsTermLocalEcho(id),
  };
  vt.onOSC = function (parm) {
    if (parm && String(parm).indexOf("666;local_echo=1") === 0) {
      tabObj.localEcho = true;
    }
  };
  TERM_TABS.push(tabObj);
  const idx = TERM_TABS.length - 1;
  tab.onclick = (e) => {
    if (e.target.classList.contains("term-tab-close")) { e.stopPropagation(); closeTermTab(TERM_TABS.indexOf(tabObj)); }
    else switchTermTab(TERM_TABS.indexOf(tabObj));
  };
  // 键盘事件绑定到隐藏 textarea（桌面+移动端统一入口）
  input.onkeydown = ev => { ev.stopPropagation(); termKeyDown(ev, tabObj); };
  // Normalize clipboard newlines to CR (PTY/readline expects \r, not bare \n).
  const termPasteText = (raw) => {
    if (!raw || !tabObj.ws) return;
    const t = String(raw).replace(/\r\n/g, "\n").replace(/\r/g, "\n").replace(/\n/g, "\r");
    if (t) {
      termFollowOutput(tabObj);
      termSend(tabObj.ws, t);
    }
  };
  // 粘贴
  input.onpaste = ev => {
    ev.preventDefault();
    termPasteText((ev.clipboardData || window.clipboardData).getData("text"));
  };
  // screen 级粘贴兜底：textarea 未聚焦时也能接收粘贴
  screen.addEventListener("paste", ev => {
    if (document.activeElement === input) return;
    ev.preventDefault();
    input.focus({ preventScroll: true });
    termPasteText((ev.clipboardData || window.clipboardData).getData("text"));
  });
  // input 事件：移动端虚拟键盘字符输入 + 桌面端可打印字符（termKeyDown 不再处理可打印字符）
  // Single commit path for IME: compositionend only clears composing; send via input.
  input.addEventListener("input", ev => {
    if (tabObj.composing || ev.isComposing) return;
    const text = input.value;
    if (text && tabObj.ws) {
      termFollowOutput(tabObj);
      termSend(tabObj.ws, text);
    }
    input.value = "";
  });
  // IME 组合输入（中文/日文等输入法）— do NOT send on compositionend (avoids double-commit).
  input.addEventListener("compositionstart", () => { tabObj.composing = true; });
  input.addEventListener("compositionend", () => {
    tabObj.composing = false;
    // Committed text lands in input.value; a following input event (or flush) sends it.
    const text = input.value;
    if (text && tabObj.ws) {
      termFollowOutput(tabObj);
      termSend(tabObj.ws, text);
      input.value = "";
    }
  });
  // beforeinput 兜底：部分移动浏览器 keydown 不触发 Backspace，用 beforeinput 捕获
  input.addEventListener("beforeinput", ev => {
    if (tabObj.composing) return;
    if (ev.inputType === "deleteContentBackward") {
      ev.preventDefault();
      if (tabObj.ws) {
        termFollowOutput(tabObj);
        termSend(tabObj.ws, "\x7f");
      }
    }
  });
  /* 鼠标与选区 —— 「终端里怎么拖都选不中、复制不了」的根因就在这几行。
   *
   * 隐藏 textarea 是键盘/输入法的入口，所以点一下终端要把焦点交给它。但 focus()
   * 会把选区上下文切到 textarea：正在进行的拖拽选区当场作废，window.getSelection()
   * 也随之变空。原来有两条抢焦点的路径都发生在**按下鼠标的瞬间**：
   *   1) mousedown 里 setTimeout(0) 主动 focus()；
   *   2) <pre tabindex=0> 被原生 mousedown 聚焦 → focus 监听再转交给 textarea。
   * 于是用户永远选不出东西来，复制、右键"复制"、Ctrl+C 一起哑火。
   *
   * 现在只在"单纯点击"（松手时没有选区）之后才把焦点交回去，按住鼠标期间一律不碰
   * 焦点；右键也不抢焦点，否则右键菜单弹出前选区就已经没了。
   */
  screen.addEventListener("mousedown", function(ev) {
    if (ev.button === 0) screen._termDragging = true;
  });
  // 松手统一由 document 上那一个监听收尾（见 ensureTermDragRelease）：松手可能落在终端外
  // （拖到窗口边缘），而且每开一个标签就往 document 上挂一个监听是只增不减的泄漏。
  ensureTermDragRelease();
  // Redirect focus to textarea; never synthesize untrusted keydown for printables
  // (preventDefault + fake KeyboardEvent drops characters on some browsers).
  screen.addEventListener("keydown", function(ev) {
    if (document.activeElement === input) return;
    input.focus({ preventScroll: true });
    const printable = ev.key && ev.key.length === 1 && !ev.ctrlKey && !ev.metaKey && !ev.altKey;
    if (printable) {
      // Let the browser deliver the character into the focused textarea → input event.
      return;
    }
    // Special keys: forward via termKeyDown on the real event after focus.
    termKeyDown(ev, tabObj);
  });
  // <pre> 被直接聚焦时（Tab 键导航），重定向到 textarea。
  // 鼠标按下期间不转交：那正是浏览器在建立选区的时刻，转交焦点会把选区清掉。
  screen.addEventListener("focus", function() {
    if (screen._termDragging) return;
    if (input && document.activeElement !== input) input.focus({ preventScroll: true });
  });
  // 右键菜单：复制 / 粘贴 / 全选 / 复制全部内容 / 重连 / 清屏
  screen.addEventListener("contextmenu", function(ev) {
    showTermContextMenu(tabObj, ev);
  });
  // JS fallback for :focus-within — toggle .term-focused class on screen
  // This ensures cursor blink animation works on iOS Safari where :focus-within
  // may not trigger for opacity:0 elements
  input.addEventListener("focus", function() {
    screen.classList.add("term-focused");
  });
  input.addEventListener("blur", function() {
    screen.classList.remove("term-focused");
  });
  bindTermVisualViewport();
  switchTermTab(idx);
  $("termMask").classList.remove("maximized");
  const mb = $("termMaxBtn"); if (mb) mb.title = I18N.t("ui.maximize_window");
  $("termMask").classList.add("show");
  ensureTermHeartbeatWorker(); // v5.4.0: 启动 Web Worker 心跳
  connectTermWS(tabObj);
}

function connectTermWS(tab) {
  tab._closed = false; // v5.4.0: clear closed flag so auto-reconnect works
  const screen = tab.screenEl, vt = tab.vt;
  setTermStatus(tab.retry > 0 ? `${I18N.t("misc.reconnecting")}(${tab.retry})` : I18N.t("ui.connecting"), "");
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const bgQ = tab.breakGlass ? "break_glass=1" : "";
  let url = `${proto}//${location.host}/api/v1/hosts/${encodeURIComponent(tab.hostId || tab.id)}/terminal`;
  if (bgQ) url += (url.includes("?")?"&":"?")+bgQ;
  if (tab.containerId) {
    const q = new URLSearchParams();
    if (tab.shell) q.set("shell", tab.shell);
    if (tab.containerName) q.set("name", tab.containerName);
    if (tab.breakGlass) q.set("break_glass", "1");
    url = `${proto}//${location.host}/api/v1/containers/${encodeURIComponent(tab.hostId || tab.id)}/${encodeURIComponent(tab.containerId)}/terminal?${q}`;
  }
  const ws = new WebSocket(url);
  ws.binaryType = "arraybuffer";
  tab.ws = ws;
  const doResize = () => {
    const s = vt.fit();
    if (!s || ws.readyState !== 1) return;
    tab._ptyCols = s.cols; tab._ptyRows = s.rows;
    termResizeSend(ws, s.cols, s.rows);
  };
  ws.onopen = () => { tab.retry = 0; tab._manualReconnect = false; setTermStatus(I18N.t("ui.connected"), "on");
    // 更新 dock 卡片状态
    const dockItem = $("termDock") && $("termDock").querySelector(`[data-tab-id="${CSS.escape(tab.id)}"]`);
    if (dockItem) { const dot = dockItem.querySelector(".dock-dot"); if (dot) { dot.className = "dock-dot on"; } }
    if (tab.inputEl) tab.inputEl.focus({ preventScroll: true }); else screen.focus();
    // Reconnect resize: 重连成功后自动发送 resize 帧，恢复终端窗口尺寸
    requestAnimationFrame(doResize);
    // Hide manual reconnect overlay if visible
    const overlay = tab.screenEl && tab.screenEl.querySelector(".term-reconnect-overlay");
    if (overlay) overlay.remove();
  };
  ws.onmessage = ev => {
    const data = new Uint8Array(ev.data);
    // Check for ZMODEM/file-transfer frame: [0xFF][0xFE][type][len:4 BE][payload]
    if (data.length >= 7 && data[0] === 0xFF && data[1] === 0xFE) {
      handleZmBrowserFrame(tab, data);
      return;
    }
    // Normal PTY output
    const text = (typeof ev.data === "string") ? ev.data : vt.dec.decode(data, { stream: true });
    vt.feed(text);
  };
  ws.onclose = (ev) => {
    setTermStatus(I18N.t("ui.disconnected"), "off");
    if (tab.ws === ws) tab.ws = null;
    // 更新 dock 卡片状态
    const dockItem = $("termDock") && $("termDock").querySelector(`[data-tab-id="${CSS.escape(tab.id)}"]`);
    if (dockItem) { const dot = dockItem.querySelector(".dock-dot"); if (dot) { dot.className = "dock-dot off"; } }

    // v5.4.0: 自动重连 — 指数退避（1s, 2s, 4s, 8s, 最大 30s），最多重试 50 次
    // 覆盖场景：浏览器后台节流、网络抖动、代理超时等导致的非主动断开
    const MAX_RETRY = 50;
    const MANUAL_THRESHOLD = 10; // 连续失败 10 次后降级为手动重连
    if (!tab._closed && tab.retry < MAX_RETRY && !tab._manualReconnect) {
      tab.retry = (tab.retry || 0) + 1;
      // 降级为手动重连模式：避免后台无限重试浪费资源
      if (tab.retry >= MANUAL_THRESHOLD) {
        tab._manualReconnect = true;
        setTermStatus(I18N.t("ui.disconnected"), "off");
        // Show clickable reconnect overlay
        const overlay = document.createElement("div");
        overlay.className = "term-reconnect-overlay";
        overlay.style.cssText = "position:absolute;inset:0;display:flex;align-items:center;justify-content:center;z-index:10;background:rgba(0,0,0,0.6);cursor:pointer;";
        overlay.innerHTML = `<div style="text-align:center;color:#ccc;font-size:14px;">
          <svg viewBox="0 0 24 24" width="32" height="32" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="margin-bottom:8px;"><path d="M21.5 2v6h-6M2.5 22v-6h6M2 11.5a10 10 0 0 1 18.8-4.3M22 12.5a10 10 0 0 1-18.8 4.2"/></svg>
          <div>${I18N.t("misc.reconnect_hint") || "点击重新连接"}</div>
        </div>`;
        overlay.addEventListener("click", () => {
          overlay.remove();
          tab._manualReconnect = false;
          tab.retry = 0;
          connectTermWS(tab);
        });
        if (tab.screenEl && tab.screenEl.isConnected) {
          tab.screenEl.style.position = "relative";
          tab.screenEl.appendChild(overlay);
        }
        return;
      }
      const delay = Math.min(1000 * Math.pow(2, tab.retry - 1), 30000);
      setTermStatus(`${I18N.t("misc.reconnecting")}(${tab.retry}/${MAX_RETRY})`, "");
      setTimeout(() => {
        if (!tab._closed && tab.screenEl && tab.screenEl.isConnected) {
          connectTermWS(tab);
        }
      }, delay);
    }
  };
  ws.onerror = () => setTermStatus(I18N.t("ui.connect_error"), "off");
}

function switchTermTab(idx) {
  if (idx < 0 || idx >= TERM_TABS.length) return;
  TERM_ACTIVE = idx;
  TERM_TABS.forEach((t, i) => { t.tabEl.classList.toggle("active", i === idx); t.screenEl.classList.toggle("active", i === idx); });
  $("termTitle").textContent = (TERM_TABS[idx].tabName || TERM_TABS[idx].name) + " " + I18N.t("term.title");
  requestAnimationFrame(() => { const t = TERM_TABS[idx]; if (t && t.inputEl) t.inputEl.focus({ preventScroll: true }); else if (t) t.screenEl.focus(); });
  if (TERM_RESIZE) window.removeEventListener("resize", TERM_RESIZE);
  TERM_RESIZE = () => termRefit();
  window.addEventListener("resize", TERM_RESIZE);
  observeTermScreen(TERM_TABS[idx].screenEl);
  // display:none tabs were never measured — refit after layout so PTY cols/rows match.
  requestAnimationFrame(() => requestAnimationFrame(() => termRefit()));
}

// window.resize misses everything that resizes the screen box without resizing
// the window: maximise, tab bar wrapping, the mobile keyboard shrinking dvh, a
// browser zoom change. Whenever the PTY thinks it is wider or taller than the
// box, the extra columns land under the scrollbar and the extra rows land below
// the fold — which is how the prompt used to disappear. Watch the box itself.
function observeTermScreen(el) {
  if (!el || typeof ResizeObserver === "undefined") return;
  if (!TERM_SCREEN_RO) {
    TERM_SCREEN_RO = new ResizeObserver(() => {
      if (TERM_RO_PENDING) return;
      TERM_RO_PENDING = requestAnimationFrame(() => { TERM_RO_PENDING = 0; termRefit(); });
    });
  }
  if (TERM_RO_TARGET === el) return;
  if (TERM_RO_TARGET) TERM_SCREEN_RO.unobserve(TERM_RO_TARGET);
  TERM_RO_TARGET = el;
  TERM_SCREEN_RO.observe(el);
}

// The mobile keyboard shrinks the visual viewport without firing window.resize,
// so on phones the full-screen modal has to track it or the prompt ends up
// behind the keyboard. Desktop must be left alone: pinning the modal to the full
// viewport height ignores the mask's 44px gutter, which made the mask overflow
// and hid the hint bar and the newest output rows below the fold.
function bindTermVisualViewport() {
  if (!window.visualViewport || TERM_VP_BOUND) return;
  TERM_VP_BOUND = true;
  const phone = window.matchMedia("(max-width:480px)");
  const apply = () => {
    const mask = $("termMask");
    if (!mask || !mask.classList.contains("show")) return;
    const modal = mask.querySelector(".term-modal");
    if (!modal) return;
    modal.style.height = phone.matches ? window.visualViewport.height + "px" : "";
  };
  window.visualViewport.addEventListener("resize", apply);
  window.visualViewport.addEventListener("scroll", apply);
  if (phone.addEventListener) phone.addEventListener("change", apply);
}

function unobserveTermScreen() {
  if (TERM_SCREEN_RO && TERM_RO_TARGET) TERM_SCREEN_RO.unobserve(TERM_RO_TARGET);
  TERM_RO_TARGET = null;
  if (TERM_RO_PENDING) { cancelAnimationFrame(TERM_RO_PENDING); TERM_RO_PENDING = 0; }
}

function closeTermTab(idx) {
  if (idx < 0 || idx >= TERM_TABS.length) return;
  const tab = TERM_TABS[idx];
  tab._closed = true; // v5.4.0: prevent auto-reconnect
  if (tab.ws) { try { tab.ws.close(); } catch(e) {} }
  tab.screenEl.remove(); tab.tabEl.remove();
  // 清理对应的 dock 卡片
  TERM_DOCK_IDS.delete(tab.id);
  const dockItem = $("termDock") && $("termDock").querySelector(`[data-tab-id="${CSS.escape(tab.id)}"]`);
  if (dockItem) dockItem.remove();
  TERM_TABS.splice(idx, 1);
  if (TERM_ACTIVE >= TERM_TABS.length) TERM_ACTIVE = TERM_TABS.length - 1;
  if (TERM_ACTIVE >= 0) switchTermTab(TERM_ACTIVE);
  else { $("termMask").classList.remove("show"); if (TERM_RESIZE) { window.removeEventListener("resize", TERM_RESIZE); TERM_RESIZE = null; } unobserveTermScreen(); }
  updateTermDock();
}

function closeAllTermTabs() {
  TERM_TABS.forEach(t => { t._closed = true; if (t.ws) { try { t.ws.close(); } catch(e) {} } });
  TERM_TABS = []; TERM_ACTIVE = -1;
  const sc = $("termScreens"); if (sc) sc.innerHTML = "";
  const tb = $("termTabbar"); if (tb) tb.innerHTML = "";
  if (TERM_RESIZE) { window.removeEventListener("resize", TERM_RESIZE); TERM_RESIZE = null; }
  unobserveTermScreen();
  clearTermDock();
}

/* ---------- 终端重连 ---------- */
function reconnectTermTab(tab) {
  if (!tab) {
    if (TERM_ACTIVE < 0 || !TERM_TABS[TERM_ACTIVE]) return;
    tab = TERM_TABS[TERM_ACTIVE];
  }
  // 关闭旧连接
  if (tab.ws) { try { tab.ws.close(); } catch(e) {} tab.ws = null; }
  tab.retry = 0;
  tab._manualReconnect = false;
  // Remove manual reconnect overlay if present
  const overlay = tab.screenEl && tab.screenEl.querySelector(".term-reconnect-overlay");
  if (overlay) overlay.remove();
  connectTermWS(tab);
}

/* ---------- 文件上传/下载（按钮交互） ---------- */
async function startTermFileUpload() {
  if (TERM_ACTIVE < 0 || !TERM_TABS[TERM_ACTIVE]) return;
  const tab = TERM_TABS[TERM_ACTIVE];
  if (!tab.ws || tab.ws.readyState !== 1) { toast(I18N.t("term.not_connected"), "err"); return; }
  // 先弹出目标目录输入（默认 /tmp/），文件名将在选择文件后自动拼接
  const targetDir = await requestAITextInput({
    title:"上传文件到远程主机",message:"选择文件前，请先指定远程目标目录。",
    label:"远程目标目录",placeholder:"/tmp/",defaultValue:"/tmp/",
    submitLabel:"选择文件",singleLine:true,maxLength:4096,danger:false,
    requiredMessage:"请输入远程目标目录"
  });
  if (!targetDir || !targetDir.trim()) return;
  // 确保目录以 / 结尾
  const dir = targetDir.trim().replace(/\/+$/, "") + "/";
  // 弹出文件选择器
  const input = document.createElement("input");
  input.type = "file";
  input.style.position = "fixed";
  input.style.left = "-9999px";
  input.style.top = "-9999px";
  document.body.appendChild(input);
  input.onchange = async () => {
    const file = input.files[0];
    document.body.removeChild(input);
    if (!file) return;
    if (file.size > 100 * 1024 * 1024) {
      toast(I18N.t("term.file_too_large"), "err");
      return;
    }
    // 自动拼接目标目录 + 文件名
    const targetPath = dir + file.name;
    toast(I18N.t("term.uploading") + ": " + file.name + " → " + targetPath + " (" + formatZmSize(file.size) + ")", "info");
    try {
      // 发送上传元数据 'f' 帧
      const meta = JSON.stringify({ filename: file.name, size: file.size, target_path: targetPath });
      const metaBytes = new TextEncoder().encode(meta);
      const metaFrame = new Uint8Array(metaBytes.length + 1);
      metaFrame[0] = 0x66; // 'f'
      metaFrame.set(metaBytes, 1);
      tab.ws.send(metaFrame);
      // 确认 WebSocket 仍然连接
      if (tab.ws.readyState !== 1) { toast(I18N.t("term.upload_cancelled"), "err"); return; }
      // 分块发送文件数据
      const buf = await file.arrayBuffer();
      const data = new Uint8Array(buf);
      const chunkSize = 32 * 1024;
      let bytesSent = 0;
      for (let offset = 0; offset < data.length; offset += chunkSize) {
        const end = Math.min(offset + chunkSize, data.length);
        termSendUpload(tab.ws, data.slice(offset, end));
        bytesSent = end;
        // 每 128KB 让出主线程，避免阻塞 WebSocket 发送缓冲区
        if (offset % (chunkSize * 4) === 0 && offset > 0) {
          await new Promise(r => setTimeout(r, 0));
        }
      }
      // 等待最后一帧被 WebSocket 发送完毕
      await new Promise(r => setTimeout(r, 50));
      termSendEnd(tab.ws);
      tab._uploadTarget = targetPath;
    } catch (err) {
      toast(I18N.t("term.upload_failed") + ": " + err.message, "err");
    }
  };
  // 对话框提交仍在用户手势链内，立即打开文件选择器，避免浏览器拦截。
  input.click();
}

async function startTermFileDownload() {
  if (TERM_ACTIVE < 0 || !TERM_TABS[TERM_ACTIVE]) return;
  const tab = TERM_TABS[TERM_ACTIVE];
  if (!tab.ws || tab.ws.readyState !== 1) { toast(I18N.t("term.not_connected"), "err"); return; }
  const remotePath = await requestAITextInput({
    title:"从远程主机下载文件",message:"请输入要下载的远程文件完整路径。",
    label:"远程文件路径",placeholder:"/var/log/syslog",defaultValue:"/tmp/",
    submitLabel:"开始下载",singleLine:true,maxLength:4096,danger:false,
    requiredMessage:"请输入远程文件路径"
  });
  if (!remotePath || !remotePath.trim()) return;
  toast(`正在请求下载: ${remotePath.trim()}`, "info");
  // 发送下载请求 'd' 帧
  const meta = JSON.stringify({ remote_path: remotePath.trim() });
  const metaBytes = new TextEncoder().encode(meta);
  const metaFrame = new Uint8Array(metaBytes.length + 1);
  metaFrame[0] = 0x64; // 'd'
  metaFrame.set(metaBytes, 1);
  tab.ws.send(metaFrame);
  // 准备接收下载数据
  tab.fileDownload = { filename: remotePath.split("/").pop() || "download.dat", chunks: [], received: 0 };
}

/* ---------- 终端收起到右下角 ---------- */
let TERM_DOCK_IDS = new Set();  // 收起的 tab id 集合

function minimizeTerminal() {
  if (TERM_TABS.length === 0) return;
  TERM_TABS.forEach(t => TERM_DOCK_IDS.add(t.id));
  const mask = $("termMask");
  if (mask) {
    const modal = mask.querySelector(".term-modal");
    if (modal) {
      modal.style.transition = "transform .2s ease, opacity .2s ease";
      modal.style.transform = "scale(.92) translateY(20px)";
      modal.style.opacity = "0";
      setTimeout(() => {
        mask.classList.remove("show", "maximized");
        modal.style.transition = "";
        modal.style.transform = "";
        modal.style.opacity = "";
      }, 200);
    } else {
      mask.classList.remove("show", "maximized");
    }
  }
  if (TERM_RESIZE) { window.removeEventListener("resize", TERM_RESIZE); TERM_RESIZE = null; }
  setTimeout(updateTermDock, 200);
}

function updateTermDock() {
  const dock = $("termDock"); if (!dock) return;
  // 移除已不存在的 tab 对应的卡片
  dock.querySelectorAll(".term-dock-item").forEach(el => {
    if (!TERM_TABS.find(t => t.id === el.dataset.tabId)) el.remove();
  });
  // 为每个收起的 tab 创建/更新卡片
  const docked = TERM_TABS.filter(t => TERM_DOCK_IDS.has(t.id));
  dock.style.display = docked.length > 0 ? "flex" : "none";
  docked.forEach(tab => {
    let item = dock.querySelector(`[data-tab-id="${CSS.escape(tab.id)}"]`);
    if (!item) {
      item = document.createElement("div");
      item.className = "term-dock-item";
      item.dataset.tabId = tab.id;
      item.innerHTML = `
        <span class="dock-dot"></span>
        <span class="dock-name"></span>
        <button class="dock-btn" title="${I18N.t('ui.expand_window')}" aria-label="${I18N.t('ui.expand_window')}">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 19V13a1 1 0 0 1 1-1h12"/><path d="M12 5l-5 7 5-7"/></svg>
        </button>
        <button class="dock-btn close" title="${I18N.t('ui.close_session')}" aria-label="${I18N.t('ui.close_session')}">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6L6 18M6 6l12 12"/></svg>
        </button>`;
      // 点击卡片主体（非按钮）也展开
      item.addEventListener("click", e => {
        if (e.target.closest(".dock-btn")) return;
        expandTermFromDock(tab.id);
      });
      item.addEventListener("dblclick", () => expandTermFromDock(tab.id));
      // 展开按钮
      item.querySelector(".dock-btn:not(.close)").addEventListener("click", e => {
        e.stopPropagation(); expandTermFromDock(tab.id);
      });
      // 关闭按钮
      item.querySelector(".dock-btn.close").addEventListener("click", e => {
        e.stopPropagation(); closeTermFromDock(tab.id);
      });
      dock.appendChild(item);
    }
    // 更新主机名 + tooltip
    const nameEl = item.querySelector(".dock-name");
    if (nameEl) {
      nameEl.textContent = tab.tabName || tab.name;
      item.title = (tab.tabName || tab.name) + " · " + I18N.t("ui.remote_terminal");
    }
    // 更新连接状态
    const dot = item.querySelector(".dock-dot");
    if (dot) {
      dot.className = "dock-dot";
      if (tab.ws && tab.ws.readyState === 1) dot.classList.add("on");
      else if (tab.ws && tab.ws.readyState === 3) dot.classList.add("off");
    }
  });
}

function expandTermFromDock(tabId) {
  const idx = TERM_TABS.findIndex(t => t.id === tabId);
  if (idx < 0) return;
  TERM_DOCK_IDS.delete(tabId);
  switchTermTab(idx);
  const mask = $("termMask");
  const modal = mask.querySelector(".term-modal");
  if (modal) {
    modal.style.transition = "transform .22s cubic-bezier(.34,1.56,.64,1), opacity .22s ease";
    modal.style.transform = "scale(.94)";
    modal.style.opacity = "0";
    mask.classList.add("show");
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        modal.style.transform = "scale(1)";
        modal.style.opacity = "1";
        setTimeout(() => {
          modal.style.transition = "";
          modal.style.transform = "";
          modal.style.opacity = "";
          // 展开动画结束后 refit + 恢复焦点
          termRefit();
          _refocusActiveTermInput();
        }, 250);
      });
    });
  } else {
    mask.classList.add("show");
  }
  requestAnimationFrame(() => requestAnimationFrame(termRefit));
  updateTermDock();
}

function closeTermFromDock(tabId) {
  const idx = TERM_TABS.findIndex(t => t.id === tabId);
  if (idx < 0) return;
  const tab = TERM_TABS[idx];
  // Close WS without triggering switchTermTab (modal is hidden — minimized state)
  if (tab.ws) { try { tab.ws.close(); } catch(e) {} }
  tab.screenEl.remove();
  tab.tabEl.remove();
  TERM_DOCK_IDS.delete(tabId);
  // Animate dock card removal
  const item = $("termDock") && $("termDock").querySelector(`[data-tab-id="${CSS.escape(tabId)}"]`);
  if (item) {
    item.classList.add("removing");
    setTimeout(() => { item.remove(); updateTermDock(); }, 200);
  }
  // Remove from array — but DON'T call switchTermTab (modal is hidden)
  TERM_TABS.splice(idx, 1);
  if (TERM_ACTIVE >= TERM_TABS.length) TERM_ACTIVE = TERM_TABS.length - 1;
  // If no tabs left, clean up fully
  if (TERM_TABS.length === 0) {
    TERM_ACTIVE = -1;
    const mask = $("termMask");
    if (mask) mask.classList.remove("show", "maximized");
    if (TERM_RESIZE) { window.removeEventListener("resize", TERM_RESIZE); TERM_RESIZE = null; }
  }
  // Immediate dock update (the animated removal happens in 200ms)
  updateTermDock();
}

function clearTermDock() {
  TERM_DOCK_IDS.clear();
  const dock = $("termDock");
  if (dock) { dock.innerHTML = ""; dock.style.display = "none"; }
}

/* ---------- 终端会话管理（列表 / 回放 / 旁观） ---------- */
let TERM_SESSIONS_TIMER = null;

function openTerminalSessions() {
  $("termSessionsMask").classList.add("show");
  loadTerminalSessions();
  if (TERM_SESSIONS_TIMER) clearInterval(TERM_SESSIONS_TIMER);
  TERM_SESSIONS_TIMER = setInterval(loadTerminalSessions, 3000);
}

let LAST_TERM_SESSIONS = [];
let TERM_SEARCH = "";

function loadTerminalSessions() {
  const mask = $("termSessionsMask");
  if (!mask || !mask.classList.contains("show")) {
    if (TERM_SESSIONS_TIMER) { clearInterval(TERM_SESSIONS_TIMER); TERM_SESSIONS_TIMER = null; }
    return;
  }
  fetch(`${API}/terminal/sessions`).then(r => r.json()).then(sessions => {
    LAST_TERM_SESSIONS = sessions || [];
    renderTerminalSessions(LAST_TERM_SESSIONS);
  }).catch(e => console.warn("load sessions:", e));
}

function renderTerminalSessions(sessions) {
  const c = $("termSessionsList");
  if (!c) return;
  // 按搜索关键词过滤
  const q = normalizeSearchText(TERM_SEARCH);
  const filtered = q ? sessions.filter(s => matchesSearchTokens(
    [s.operator, s.hostname, s.ip, s.host_id, s.id].filter(Boolean).join(" "),
    TERM_SEARCH
  )) : sessions;
  // 更新计数
  const cnt = $("termSessionCount");
  if (cnt) {
    cnt.textContent = q ? `${filtered.length}/${sessions.length} 条` : `${sessions.length} 条`;
  }
  if (filtered.length === 0) {
    c.innerHTML = `<div style="text-align:center; color:var(--muted2); padding:32px 0">${q ? I18N.t("empty.no_terminal_match") : I18N.t("empty.no_active_sessions")}</div>`;
    return;
  }
  c.innerHTML = filtered.map(s => {
    const t = new Date(s.created_at * 1000);
    const time = `${String(t.getHours()).padStart(2,'0')}:${String(t.getMinutes()).padStart(2,'0')}:${String(t.getSeconds()).padStart(2,'0')}`;
    const ipStr = s.ip ? ` · IP ${esc(s.ip)}` : "";
    return `<div class="term-session-item">
      <div class="term-session-info">
        <div class="term-session-host">${esc(s.hostname)}</div>
        <div class="term-session-meta">${I18N.t("section.operator")} <strong style="color:var(--accent-txt)">${esc(s.operator)}</strong>${ipStr}${I18N.t("section.start_label")}${time} · ${s.frames} ${I18N.t("ui.frames_recorded")}</div>
      </div>
      ${s.observers > 0 ? `<span class="term-session-badge observers">${s.observers} ${I18N.t("ui.observe")}</span>` : `<span class="term-session-badge">${I18N.t("ui.active")}</span>`}
      <div class="term-session-actions">
        <button class="btn sm" data-act="term-observe" data-sid="${esc(s.id)}" data-host="${esc(s.hostname)}">${I18N.t("ui.observe")}</button>
        <button class="btn sm" data-act="term-replay" data-sid="${esc(s.id)}" data-host="${esc(s.hostname)}">${I18N.t("ui.replay")}</button>
      </div>
    </div>`;
  }).join("");
}

function showTermSessionsTab() {
  const sp = $("termSessionsPane"), cp = $("termCommandsPane");
  const ts = $("termTabSessions"), tc = $("termTabCommands");
  if (sp) sp.style.display = "";
  if (cp) cp.style.display = "none";
  if (ts) ts.classList.add("active");
  if (tc) tc.classList.remove("active");
}
function showTermCommandsTab() {
  const sp = $("termSessionsPane"), cp = $("termCommandsPane");
  const ts = $("termTabSessions"), tc = $("termTabCommands");
  if (sp) sp.style.display = "none";
  if (cp) cp.style.display = "";
  if (ts) ts.classList.remove("active");
  if (tc) tc.classList.add("active");
  loadTerminalCommands();
}
async function loadTerminalCommands() {
  const list = $("termCommandsList"); if (!list) return;
  const host = ($("termCmdHost") && $("termCmdHost").value || "").trim();
  const actor = ($("termCmdActor") && $("termCmdActor").value || "").trim();
  const q = ($("termCmdQ") && $("termCmdQ").value || "").trim();
  const params = new URLSearchParams({ limit: "100", offset: "0" });
  if (host) params.set("host", host);
  if (actor) params.set("actor", actor);
  if (q) params.set("q", q);
  try {
    const j = await fetch(`${API}/terminal/commands?${params}`).then(r => r.json());
    const items = j.items || [];
    const cnt = $("termCmdCount");
    if (cnt) cnt.textContent = `共 ${j.total || items.length} 条（永久审计）`;
    if (!items.length) {
      list.innerHTML = `<div style="text-align:center;color:var(--muted2);padding:24px 0">暂无终端命令记录</div>`;
      return;
    }
    list.innerHTML = items.map(it => {
      const t = new Date((it.timestamp || 0) * 1000);
      const time = isNaN(t.getTime()) ? "-" : t.toLocaleString();
      return `<div class="term-session-item" style="align-items:flex-start">
        <div class="term-session-info" style="flex:1">
          <div class="term-session-host mono" style="font-size:12px;white-space:pre-wrap;word-break:break-all">${esc(it.message || "")}</div>
          <div class="term-session-meta">${esc(it.actor || "-")} · ${esc(it.host || "-")}${it.ip ? " · " + esc(it.ip) : ""} · ${time}</div>
        </div>
      </div>`;
    }).join("");
  } catch (e) {
    list.innerHTML = `<div class="empty-line">加载失败：${esc(String(e))}</div>`;
  }
}
safeAddEventListener("termTabSessions", "click", showTermSessionsTab);
safeAddEventListener("termTabCommands", "click", showTermCommandsTab);
safeAddEventListener("termCmdSearchBtn", "click", loadTerminalCommands);

/* ---------- 终端回放 ---------- */
let REPLAY = null; // {frames, idx, vt, timer, playing, speed}

function openTerminalReplay(sessionId, hostname) {
  fetch(`${API}/terminal/sessions/${encodeURIComponent(sessionId)}/replay`)
    .then(r => r.json())
    .then(data => {
      // Replay OUTPUT frames (shell output) + RESIZE frames (terminal dimension changes).
      // INPUT frames are excluded: the shell output already contains the command echo.
      const frames = (data.frames || []).filter(f => f.type === "output" || f.type === "resize");
      if (frames.length === 0) { toast(I18N.t("empty.no_recording"), "err"); return; }
      // 从第一个 resize 帧获取录制时的初始终端尺寸
      let initCols = 80, initRows = 24;
      for (const f of frames) {
        if (f.type === "resize") {
          try {
            const parts = atob(f.data).split("x");
            const c = parseInt(parts[0]), r = parseInt(parts[1]);
            if (c >= 20 && r >= 6) { initCols = c; initRows = r; }
          } catch (e) {}
          break;
        }
      }
      $("termReplayTitle").textContent = hostname + " " + I18N.t("term.replay_title");
      const screen = $("termReplayScreen");
      const vt = makeVT(screen);
      // 用录制时的终端尺寸初始化 VT，避免 80x24 默认值导致换行错位
      if (initCols !== 80 || initRows !== 24) {
        vt.resizeTo(initCols, initRows);
      }
      screen._vt = vt;
      REPLAY = {frames, idx: 0, vt, timer: null, playing: false, speed: 2};
      $("termReplayMask").classList.add("show");
      $("termSessionsMask").classList.remove("show");
      if (TERM_SESSIONS_TIMER) { clearInterval(TERM_SESSIONS_TIMER); TERM_SESSIONS_TIMER = null; }
      document.querySelectorAll(".replay-speed-btn").forEach(b => {
        b.classList.toggle("active", parseFloat(b.dataset.speed) === 2);
      });
      updateReplayProgress();
      playReplay();
    })
    .catch(e => toast(I18N.t("toast.load_replay_failed") + e, "err"));
}

function playReplay() {
  if (!REPLAY || REPLAY.playing) return;
  REPLAY.playing = true;
  const btn = $("replayPlayBtn"); if (btn) btn.textContent = "⏸";
  const st = $("replayStatus"); if (st) { st.textContent = I18N.t("ui.playing"); st.className = "term-status on"; }
  scheduleNextFrame();
}

function pauseReplay() {
  if (!REPLAY) return;
  REPLAY.playing = false;
  if (REPLAY.timer) { clearTimeout(REPLAY.timer); REPLAY.timer = null; }
  const btn = $("replayPlayBtn"); if (btn) btn.textContent = "▶";
  const st = $("replayStatus"); if (st) { st.textContent = I18N.t("ui.paused"); st.className = "term-status"; }
}

function scheduleNextFrame() {
  if (!REPLAY || !REPLAY.playing) return;
  if (REPLAY.idx >= REPLAY.frames.length) {
    REPLAY.playing = false;
    const btn = $("replayPlayBtn"); if (btn) btn.textContent = "▶";
    const st = $("replayStatus"); if (st) { st.textContent = I18N.t("ui.playback_done"); st.className = "term-status"; }
    updateReplayProgress();
    return;
  }
  const frame = REPLAY.frames[REPLAY.idx];
  const bytes = Uint8Array.from(atob(frame.data), c => c.charCodeAt(0));
  if (frame.type === "resize") {
    // resize 帧：解析 cols/rows 并调整 VT 网格，不 feed 文本
    const parts = new TextDecoder().decode(bytes).split("x");
    const c = parseInt(parts[0]), r = parseInt(parts[1]);
    if (c >= 20 && r >= 6) REPLAY.vt.resizeTo(c, r);
  } else {
    const text = REPLAY.vt.dec.decode(bytes, { stream: true });
    REPLAY.vt.feed(text);
  }
  REPLAY.idx++;
  updateReplayProgress();
  let delay = 0;
  if (REPLAY.idx < REPLAY.frames.length) {
    const next = REPLAY.frames[REPLAY.idx];
    delay = (next.ts - frame.ts) / REPLAY.speed;
    delay = Math.min(Math.max(delay, 1), 3000 / REPLAY.speed);
  }
  REPLAY.timer = setTimeout(scheduleNextFrame, delay);
}

function setReplaySpeed(speed) {
  if (!REPLAY) return;
  REPLAY.speed = speed;
  document.querySelectorAll(".replay-speed-btn").forEach(b => {
    b.classList.toggle("active", parseFloat(b.dataset.speed) === speed);
  });
}

function seekReplay(progress) {
  if (!REPLAY) return;
  pauseReplay();
  const targetIdx = Math.floor(progress * REPLAY.frames.length);
  // 从头回放：先用第一个 resize 帧确定初始尺寸
  let initCols = 80, initRows = 24;
  for (const f of REPLAY.frames) {
    if (f.type === "resize") {
      try {
        const parts = atob(f.data).split("x");
        const c = parseInt(parts[0]), r = parseInt(parts[1]);
        if (c >= 20 && r >= 6) { initCols = c; initRows = r; }
      } catch (e) {}
      break;
    }
  }
  const screen = $("termReplayScreen");
  const vt = makeVT(screen);
  if (initCols !== 80 || initRows !== 24) vt.resizeTo(initCols, initRows);
  screen._vt = vt;
  REPLAY.vt = vt;
  REPLAY.idx = 0;
  for (let i = 0; i < targetIdx; i++) {
    const frame = REPLAY.frames[i];
    const bytes = Uint8Array.from(atob(frame.data), c => c.charCodeAt(0));
    if (frame.type === "resize") {
      const parts = new TextDecoder().decode(bytes).split("x");
      const c = parseInt(parts[0]), r = parseInt(parts[1]);
      if (c >= 20 && r >= 6) vt.resizeTo(c, r);
    } else {
      const text = vt.dec.decode(bytes, { stream: true });
      vt.feed(text);
    }
  }
  REPLAY.idx = targetIdx;
  updateReplayProgress();
}

function updateReplayProgress() {
  if (!REPLAY) return;
  const pct = REPLAY.frames.length > 0 ? (REPLAY.idx / REPLAY.frames.length) * 100 : 0;
  const bar = $("replayProgress"); if (bar) bar.style.width = pct + "%";
  const time = $("replayTime"); if (time) time.textContent = `${REPLAY.idx} / ${REPLAY.frames.length} 帧`;
}

function closeReplay() { pauseReplay(); REPLAY = null; }

/* ---------- 终端只读旁观 ---------- */
let OBSERVE_WS = null;

function openTerminalObserve(sessionId, hostname) {
  const screen = $("termObserveScreen");
  const vt = makeVT(screen);
  screen._vt = vt;
  $("termObserveTitle").textContent = hostname + " " + I18N.t("term.observe_title");
  setObserveStatus(I18N.t("ui.connecting"), "");
  $("termObserveMask").classList.add("show");
  $("termSessionsMask").classList.remove("show");
  if (TERM_SESSIONS_TIMER) { clearInterval(TERM_SESSIONS_TIMER); TERM_SESSIONS_TIMER = null; }
  closeObserveWS();
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(`${proto}//${location.host}/api/v1/terminal/sessions/${encodeURIComponent(sessionId)}/observe`);
  ws.binaryType = "arraybuffer";
  OBSERVE_WS = ws;
  ws.onopen = () => setObserveStatus(I18N.t("ui.observing"), "on");
  ws.onmessage = ev => {
    const text = (typeof ev.data === "string") ? ev.data : vt.dec.decode(new Uint8Array(ev.data), { stream: true });
    vt.feed(text);
  };
  ws.onclose = () => setObserveStatus(I18N.t("ui.session_ended"), "off");
  ws.onerror = () => setObserveStatus(I18N.t("ui.connect_error"), "off");
}

function closeObserveWS() {
  if (OBSERVE_WS) { try { OBSERVE_WS.close(); } catch(e) {} OBSERVE_WS = null; }
}

function setObserveStatus(txt, cls) {
  const s = $("observeStatus"); if (s) { s.textContent = txt; s.className = "term-status" + (cls ? " " + cls : ""); }
}
// 发送窗口尺寸（帧首字节 'r'，负载 "colsxrows"）→ 服务端 → Agent → PTY
function termResizeSend(ws, cols, rows) {
  if (!ws || ws.readyState !== 1) return;
  const body = new TextEncoder().encode(cols + "x" + rows);
  const framed = new Uint8Array(body.length + 1);
  framed[0] = 0x72; // 'r'
  framed.set(body, 1);
  ws.send(framed);
}
// 重新测量终端并把新尺寸告知 PTY（放大/还原/窗口变化后调用）
// resize 后自动恢复输入焦点，防止光标丢失
function termRefit() {
  if (TERM_ACTIVE < 0 || !TERM_TABS[TERM_ACTIVE]) return;
  const tab = TERM_TABS[TERM_ACTIVE];
  if (tab.vt && tab.ws) {
    const s = tab.vt.fit();
    // The ResizeObserver fires for sub-pixel reflows too; only tell the PTY when
    // the grid actually changed, or a burst of SIGWINCH will make apps redraw.
    if (s && tab.ws.readyState === 1 && (s.cols !== tab._ptyCols || s.rows !== tab._ptyRows)) {
      tab._ptyCols = s.cols; tab._ptyRows = s.rows;
      termResizeSend(tab.ws, s.cols, s.rows);
    }
  }
  // 确保 resize 后焦点回到输入框
  _refocusActiveTermInput();
}
// 放大 / 还原 终端窗口
safeAddEventListener("termMaxBtn", "click", () => {
  const mask = $("termMask"); if (!mask) return;
  const max = mask.classList.toggle("maximized");
  const btn = $("termMaxBtn"); if (btn) btn.title = max ? I18N.t("ui.restore_size") : I18N.t("ui.maximize_window");
  // 等布局稳定后 refit + 恢复焦点（双层 rAF 确保浏览器完成布局）
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      termRefit();
      _refocusActiveTermInput();
    });
  });
});
// 收起到右下角
safeAddEventListener("termMinBtn", "click", () => {
  minimizeTerminal();
});
// 文件上传
safeAddEventListener("termUploadBtn", "click", () => startTermFileUpload());
// 文件下载
safeAddEventListener("termDownloadBtn", "click", () => startTermFileDownload());

/* ---------- v5.3.0: 终端二次认证弹窗事件绑定 ---------- */
// 协议弹窗：勾选启用继续按钮
safeAddEventListener("termProtocolAgree", "change", function() {
  $("termProtocolContinue").disabled = !this.checked;
});
// 协议弹窗：同意并继续
safeAddEventListener("termProtocolContinue", "click", onTermProtocolAgreed);
// 协议弹窗：关闭（取消）
$("termProtocolMask").addEventListener("click", function(e) {
  if (e.target === this || e.target.closest("[data-close-btn]")) {
    cancelTermAuth();
  }
});

// 密码设置弹窗：提交
safeAddEventListener("termSetPwdBtn", "click", submitTermSetPassword);
// 密码设置弹窗：取消
safeAddEventListener("termSetPwdCancel", "click", function() {
  $("termSetPwdMask").classList.remove("show");
  cancelTermAuth();
});
$("termSetPwdMask").addEventListener("click", function(e) {
  if (e.target === this || e.target.closest("[data-close-btn]")) {
    $("termSetPwdMask").classList.remove("show");
    cancelTermAuth();
  }
});
// 密码设置弹窗：回车提交
safeAddEventListener("termSetPwd", "keydown", function(e) { if (e.key === "Enter") submitTermSetPassword(); });
safeAddEventListener("termSetPwd2", "keydown", function(e) { if (e.key === "Enter") submitTermSetPassword(); });
// 密码设置弹窗：显示/隐藏密码
safeAddEventListener("termSetPwdToggle", "click", function() { toggleTermPwdVisibility("termSetPwd", "termSetPwdToggle"); });
safeAddEventListener("termSetPwd2Toggle", "click", function() { toggleTermPwdVisibility("termSetPwd2", "termSetPwd2Toggle"); });

// 密码验证弹窗：提交
safeAddEventListener("termVerifyBtn", "click", submitTermVerify);
// 密码验证弹窗：取消
safeAddEventListener("termVerifyCancel", "click", function() {
  $("termVerifyMask").classList.remove("show");
  cancelTermAuth();
});
$("termVerifyMask").addEventListener("click", function(e) {
  if (e.target === this || e.target.closest("[data-close-btn]")) {
    $("termVerifyMask").classList.remove("show");
    cancelTermAuth();
  }
});
// 密码验证弹窗：回车提交
safeAddEventListener("termVerifyPwd", "keydown", function(e) { if (e.key === "Enter") submitTermVerify(); });
// 密码验证弹窗：显示/隐藏密码
safeAddEventListener("termVerifyPwdToggle", "click", function() { toggleTermPwdVisibility("termVerifyPwd", "termVerifyPwdToggle"); });

// 锁定弹窗：关闭
$("termLockedMask").addEventListener("click", function(e) {
  if (e.target === this || e.target.closest("[data-close-btn]")) {
    $("termLockedMask").classList.remove("show");
    cancelTermAuth();
  }
});

function setTermStatus(txt, cls) {
  const s = $("termStatus"); if (s) { s.textContent = txt; s.className = "term-status" + (cls ? " " + cls : ""); }
  // 同步更新当前活动 tab 的 dock 卡片状态
  if (TERM_ACTIVE >= 0 && TERM_TABS[TERM_ACTIVE]) {
    const tab = TERM_TABS[TERM_ACTIVE];
    const item = $("termDock") && $("termDock").querySelector(`[data-tab-id="${CSS.escape(tab.id)}"]`);
    if (item) {
      const dot = item.querySelector(".dock-dot");
      if (dot) {
        dot.className = "dock-dot";
        if (cls === "on") dot.classList.add("on");
        else if (cls === "off") dot.classList.add("off");
      }
    }
  }
}
function closeTerminalWS() { closeAllTermTabs(); }
// 发送输入（帧首字节 'i' 标识 input）
function termSend(ws, str) {
  if (!ws || ws.readyState !== 1) return;
  const tab = TERM_TABS.find(t => t && t.ws === ws);
  if (tab) termLocalEcho(tab, str);
  const body = new TextEncoder().encode(str);
  const framed = new Uint8Array(body.length + 1);
  framed[0] = 0x69; // 'i'
  framed.set(body, 1);
  ws.send(framed);
}
// Typing / paste must jump back to the live prompt — same as a real TTY.
function termFollowOutput(tab) {
  if (tab && tab.vt && typeof tab.vt.followOutput === "function") tab.vt.followOutput();
}
// 发送上传数据块（帧首字节 'u'）
function termSendUpload(ws, chunk) {
  if (!ws || ws.readyState !== 1) return;
  const framed = new Uint8Array(chunk.length + 1);
  framed[0] = 0x75; // 'u'
  framed.set(chunk, 1);
  ws.send(framed);
}
// 发送上传结束信号（帧首字节 'e'）
function termSendEnd(ws) {
  if (!ws || ws.readyState !== 1) return;
  ws.send(new Uint8Array([0x65])); // 'e'
}
// ---- ZMODEM/文件传输 浏览器端帧处理 ----
// handleZmBrowserFrame 解析并处理来自 Agent 的 ZMODEM/文件传输帧。
// 帧格式: [0xFF][0xFE][type][len:4 BE][payload]
function handleZmBrowserFrame(tab, data) {
  const zmType = data[2];
  const zmLen = (data[3] << 24) | (data[4] << 16) | (data[5] << 8) | data[6];
  const zmPayload = data.slice(7, 7 + zmLen);
  switch (zmType) {
    case 0x5A: { // 'Z' — ZMODEM 信号
      const info = new TextDecoder().decode(zmPayload);
      let meta;
      try { meta = JSON.parse(info); } catch (e) { return; }
      if (meta.type === "sz") {
        // 下载：准备接收文件数据
        tab.zmDownload = { filename: meta.filename || "download.dat", size: meta.size || 0, chunks: [], received: 0 };
        toast(I18N.t("term.downloading") + ": " + tab.zmDownload.filename + " (" + formatZmSize(tab.zmDownload.size) + ")", "info");
      } else if (meta.type === "rz") {
        // 上传：弹出文件选择对话框
        showZmUploadDialog(tab);
      }
      break;
    }
    case 0x46: { // 'F' — 文件信息（按钮上传ACK或下载元数据）
      const info = new TextDecoder().decode(zmPayload);
      let meta;
      try { meta = JSON.parse(info); } catch (e) { return; }
      if (meta.type === "upload_ack") {
        if (meta.status === "ok") {
          toast(`上传完成: ${meta.filename || ""}`, "ok");
        } else {
          toast(`上传失败: ${meta.message || "未知错误"}`, "err");
        }
      } else if (meta.type === "download_meta") {
        // 下载元数据：准备接收文件数据
        tab.fileDownload = tab.fileDownload || {};
        tab.fileDownload.filename = meta.filename || "download.dat";
        tab.fileDownload.size = meta.size || 0;
        tab.fileDownload.chunks = [];
        tab.fileDownload.received = 0;
        toast(`正在下载: ${meta.filename} (${formatZmSize(meta.size)})`, "info");
      } else if (meta.type === "download_error") {
        toast(`下载失败: ${meta.message || "未知错误"}`, "err");
        tab.fileDownload = null;
      }
      break;
    }
    case 0x44: // 'D' — 下载数据块
      if (tab.zmDownload) {
        tab.zmDownload.chunks.push(zmPayload);
        tab.zmDownload.received += zmPayload.length;
      }
      if (tab.fileDownload) {
        tab.fileDownload.chunks.push(zmPayload);
        tab.fileDownload.received += zmPayload.length;
      }
      break;
    case 0x45: // 'E' — 传输完成
      if (tab.zmDownload) {
        const dl = tab.zmDownload;
        const blob = new Blob(dl.chunks);
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = dl.filename;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        setTimeout(() => URL.revokeObjectURL(url), 1000);
        toast(I18N.t("term.download_done") + ": " + dl.filename, "ok");
        tab.zmDownload = null;
      }
      if (tab.fileDownload) {
        const dl = tab.fileDownload;
        const blob = new Blob(dl.chunks);
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = dl.filename;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        setTimeout(() => URL.revokeObjectURL(url), 1000);
        toast(`下载完成: ${dl.filename}`, "ok");
        tab.fileDownload = null;
      }
      break;
  }
}
// showZmUploadDialog 弹出文件选择对话框，读取文件并通过 WebSocket 上传。
function showZmUploadDialog(tab) {
  const input = document.createElement("input");
  input.type = "file";
  input.style.display = "none";
  document.body.appendChild(input);
  input.onchange = async () => {
    const file = input.files[0];
    document.body.removeChild(input);
    if (!file) return;
    if (file.size > 100 * 1024 * 1024) {
      toast(I18N.t("term.file_too_large"), "err");
      return;
    }
    toast(I18N.t("term.uploading") + ": " + file.name + " (" + formatZmSize(file.size) + ")", "info");
    try {
      const buf = await file.arrayBuffer();
      const data = new Uint8Array(buf);
      const chunkSize = 32 * 1024; // 32KB chunks
      for (let offset = 0; offset < data.length; offset += chunkSize) {
        const end = Math.min(offset + chunkSize, data.length);
        termSendUpload(tab.ws, data.slice(offset, end));
      }
      termSendEnd(tab.ws);
    } catch (err) {
      toast(I18N.t("term.upload_failed") + ": " + err.message, "err");
    }
  };
  input.click();
}
// formatZmSize 格式化文件大小显示
function formatZmSize(bytes) {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
  return (bytes / (1024 * 1024)).toFixed(1) + " MB";
}
// normalizeTermCopyText 规整复制出去的终端文本：统一换行、去掉每行尾部的填充空格。
// 终端每行都补齐到列宽，直接复制会带一串看不见的空格，粘进编辑器/工单里很难看。
function normalizeTermCopyText(text) {
  return String(text || "")
    .replace(/\r\n/g, "\n")
    .split("\n")
    .map(line => line.replace(/[ \t\u00a0]+$/, ""))
    .join("\n");
}

// getSelectedTermText 获取当前终端屏幕内的选中文本。
//
// 两处容易踩空：
//   1) 焦点在隐藏 textarea 上时，浏览器为表单元素维护独立的选区上下文，
//      window.getSelection().rangeCount 为 0——临时 blur 再读，读完把焦点还回去；
//   2) 必须用 Selection.toString() 而不是 Range.toString()。后者只把文本节点拼起来，
//      不认块级边界，跨行选中会被拼成一整行——整屏命令粘出来是一条长龙。
function getSelectedTermText(tab) {
  const read = () => {
    const s = window.getSelection();
    if (!s || s.rangeCount === 0) return "";
    if (!tab || !tab.screenEl) return "";
    let inScreen = false;
    for (let i = 0; i < s.rangeCount; i++) {
      if (tab.screenEl.contains(s.getRangeAt(i).commonAncestorContainer)) { inScreen = true; break; }
    }
    if (!inScreen) return "";
    return s.toString();
  };
  let text = read();
  if (!text) {
    const ae = document.activeElement;
    if (ae && ae.classList.contains("term-input")) {
      ae.blur();
      try { text = read(); } finally { ae.focus({ preventScroll: true }); }
    }
  }
  return text;
}

// termSelectionText 选区文本（已规整）——判空也走它，保证"有没有选区"与"复制什么"同一套判断。
function termSelectionText(tab) {
  return normalizeTermCopyText(getSelectedTermText(tab)).replace(/\n+$/, "");
}

// termScreenText 整个终端窗口的文本：回滚缓冲 + 当前屏，按 DOM 顺序逐行取。
function termScreenText(tab) {
  if (!tab || !tab.screenEl) return "";
  const rows = tab.screenEl.querySelectorAll(".term-row");
  const lines = [];
  rows.forEach(row => lines.push(row.textContent || ""));
  return normalizeTermCopyText(lines.join("\n")).replace(/\n+$/, "");
}

// termSelectAllScreen 选中整窗内容（右键菜单「全选」）。
// 先 blur 隐藏 textarea：焦点在表单元素上时，加进去的 Range 落在另一个选区上下文里，
// 用户看不到高亮，复制也读不到。
function termSelectAllScreen(tab) {
  if (!tab || !tab.screenEl) return false;
  const rows = tab.screenEl.querySelectorAll(".term-row");
  if (!rows.length) return false;
  const ae = document.activeElement;
  if (ae && ae.classList.contains("term-input")) ae.blur();
  const range = document.createRange();
  range.setStartBefore(rows[0]);
  range.setEndAfter(rows[rows.length - 1]);
  const sel = window.getSelection();
  if (!sel) return false;
  sel.removeAllRanges();
  sel.addRange(range);
  return true;
}

// termCopyText 复制到剪贴板并给出反馈。
// 走 copyToClipboard：HTTPS 下用 navigator.clipboard，明文 HTTP（非安全上下文，
// navigator.clipboard 根本不存在）退回 execCommand。以前这里各写各的，
// 明文部署下右键复制是静默失败——用户只知道"复制不了"，看不到任何提示。
function termCopyText(text) {
  const out = normalizeTermCopyText(text);
  if (!out) { toast(I18N.t("term.nothing_selected", "没有选中内容"), "info"); return; }
  copyToClipboard(out).then(
    () => toast(I18N.t("toast.copied"), "ok"),
    () => toast(I18N.t("toast.copy_failed"), "err")
  );
}

// ---- 拖拽选区收尾（全局仅一份）----
// 只在"单纯点击"之后把焦点交回隐藏 textarea；拖出了选区就保住选区。
// 松手事件挂在 document 上：用户经常拖到终端框外面才松手，那时 screen 收不到 mouseup。
let TERM_DRAG_RELEASE_BOUND = false;
function ensureTermDragRelease() {
  if (TERM_DRAG_RELEASE_BOUND) return;
  TERM_DRAG_RELEASE_BOUND = true;
  document.addEventListener("mouseup", function(ev) {
    TERM_TABS.forEach(tab => {
      const screen = tab && tab.screenEl;
      if (!screen || !screen._termDragging) return;
      screen._termDragging = false;
      if (ev && ev.button !== 0) return;
      if (!screen.isConnected) return;
      if (termSelectionText(tab)) return;               // 有选区 → 保住它
      if (tab.inputEl && document.activeElement !== tab.inputEl) {
        tab.inputEl.focus({ preventScroll: true });
      }
    });
  });
}

// ---- 全局 copy 事件处理（终端文本复制）----
// 仅注册一次。用户在终端里按 Ctrl+C / 用浏览器菜单复制时，把规整过的选区文本写进
// 剪贴板（去掉行尾填充空格、保留换行）。
(function() {
  document.addEventListener("copy", function(ev) {
    const activeTab = TERM_ACTIVE >= 0 ? TERM_TABS[TERM_ACTIVE] : null;
    if (!activeTab || !activeTab.screenEl) return;
    const mask = document.getElementById("termMask");
    if (!mask || !mask.classList.contains("show")) return;
    const sel = termSelectionText(activeTab);
    if (!sel) return;
    ev.preventDefault();
    ev.clipboardData.setData("text/plain", sel);
  });
})();

function termKeyDown(e, tab) {
  e.stopPropagation(); // 阻止全局 Esc 关弹窗，让 Esc 等按键传给 shell
  // IME composition: never inject Enter/Backspace/arrows into the PTY.
  if (tab && (tab.composing || e.isComposing)) return;
  const ws = tab ? tab.ws : null;
  const k = e.key;
  const mod = e.ctrlKey || e.metaKey;

  // ====== P0: 剪贴板双向交互 ======

  // Ctrl+V / Cmd+V / Shift+Insert → 粘贴剪贴板内容
  // 关键：不调用 preventDefault()，让浏览器原生 paste 事件正常触发，
  // 由 input.onpaste / screen paste 兜底处理器捕获文本并发送。
  if ((mod && (k === "v" || k === "V")) || (e.shiftKey && k === "Insert")) {
    return; // 放行浏览器 paste，不发送 \x16
  }

  // Ctrl+C / Cmd+C → 有选区时复制，无选区时发送 SIGINT
  // Ctrl+Shift+C / Cmd+Shift+C / Ctrl+Insert → 强制复制
  if ((mod && (k === "c" || k === "C")) || (mod && k === "Insert")) {
    const shiftCopy = e.shiftKey;
    const sel = termSelectionText(tab);
    if (sel) {
      e.preventDefault();
      termCopyText(sel);
      return;
    }
    if (shiftCopy) {
      // 明确要复制却没有选区：给一句话，而不是把 ^C 打进 shell。
      e.preventDefault();
      toast(I18N.t("term.nothing_selected", "没有选中内容"), "info");
      return;
    }
    // 无选区 → 发送 SIGINT (\x03)
  }

  const ac = (tab && tab.vt && tab.vt.appCursor) ? "\x1bO" : "\x1b["; // 应用光标模式(vim/less…)
  let seq = null;
  if (k === "Enter") seq = "\r";
  else if (k === "Backspace") seq = "\x7f";
  else if (k === "Tab") seq = e.shiftKey ? "\x1b[Z" : "\t";
  else if (k === "Escape") seq = "\x1b";
  else if (k === "ArrowUp") seq = ac + "A";
  else if (k === "ArrowDown") seq = ac + "B";
  else if (k === "ArrowRight") seq = ac + "C";
  else if (k === "ArrowLeft") seq = ac + "D";
  else if (k === "Home") seq = ac + "H";
  else if (k === "End") seq = ac + "F";
  else if (k === "Delete") seq = "\x1b[3~";
  else if (k === "PageUp") seq = "\x1b[5~";
  else if (k === "PageDown") seq = "\x1b[6~";
  else if (e.ctrlKey && k.length === 1) {
    const c = k.toLowerCase().charCodeAt(0);
    if (c >= 97 && c <= 122) seq = String.fromCharCode(c - 96); // Ctrl+A..Z → 0x01..0x1A
  }
  // 可打印字符不再由 keydown 处理——改由隐藏 textarea 的 input 事件统一处理。
  // 原因：移动端虚拟键盘的 keydown e.key 常为 "Unidentified"，不可靠；
  //       input 事件在所有平台都能正确获取实际输入文本。
  // 桌面端：keydown 不 preventDefault → 字符进入 textarea → input 事件发送 → 清空 textarea
  // 移动端：keydown 可能不识别 → 同样由 input 事件兜底发送
  if (seq !== null) {
    e.preventDefault();
    termFollowOutput(tab);
    termSend(ws, seq);
  }
}
/* ---------- 阶段2：VT100 / xterm 子集终端仿真器 ----------
   支持屏幕缓冲 + 光标寻址(CUP/CUU…)、擦除(ED/EL)、SGR 颜色(16/256/RGB、粗体/下划线/反显)、
   滚动区(DECSTBM)、插入/删除行列、备用屏(?1049)、回滚缓冲，可跑 vim/top 等全屏程序。 */
const VT_PAL = [
  "#2b303b", "#ff6b72", "#4fd483", "#e8b84b", "#5b9bff", "#c88bf0", "#4fc3f0", "#c8ced8",
  "#5a6473", "#ff8f95", "#7ee6a5", "#ffd071", "#82b4ff", "#d9b3f7", "#8fd7f7", "#ffffff"
];
function vt256(n) {
  n = n | 0;
  if (n < 16) return VT_PAL[n] || null;
  if (n < 232) { n -= 16; const r = Math.floor(n / 36), g = Math.floor((n % 36) / 6), b = n % 6; const c = v => v ? 55 + v * 40 : 0; return `rgb(${c(r)},${c(g)},${c(b)})`; }
  const v = 8 + (n - 232) * 10; return `rgb(${v},${v},${v})`;
}
const vtEsc = s => s.replace(/[&<>]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" }[c]));

function makeVT(screen) {
  const vt = {
    screen, dec: new TextDecoder("utf-8"),
    cols: 80, rows: 24, cx: 0, cy: 0,
    fg: null, bg: null, flags: 0,          // flags: 1 粗体 2 反显 4 下划线 8 弱化
    sCx: 0, sCy: 0, sFg: null, sBg: null, sFlags: 0,
    top: 0, bot: 23, wrapNext: false,
    grid: null, SB_MAX: 2000,
    altActive: false, savedGrid: null, savedPos: null,
    st: 0, parm: "", coll: "",             // 解析状态 0 ground 1 esc 2 csi 3 osc 4 charset 5 osc-st
    cursorVis: true, appCursor: false, raf: 0, _rowCache: null,
    _cellW: 0, _cellH: 0, // monospace cell metrics for cursor overlay
    // Follow the live prompt unless the user deliberately scrolls into scrollback.
    // Checking distance AFTER a burst of output is wrong: one chunk can grow the
    // scrollback by hundreds of px, so a post-hoc "dist < 48" check loses the pin
    // and leaves the cursor / prompt below the fold.
    _pinBottom: true,
  };
  const clampX = x => Math.max(0, Math.min(vt.cols - 1, x));
  const clampY = y => Math.max(0, Math.min(vt.rows - 1, y));
  const blank = () => ({ c: " ", f: null, b: null, a: 0 });
  const newRow = () => { const r = new Array(vt.cols); for (let i = 0; i < vt.cols; i++) r[i] = blank(); return r; };
  function alloc() { vt.grid = []; for (let y = 0; y < vt.rows; y++) vt.grid.push(newRow()); }

  screen.innerHTML = "";
  const sb = document.createElement("div"); sb.className = "term-sb";
  const lv = document.createElement("div"); lv.className = "term-lv";
  const cursorOverlay = document.createElement("span"); cursorOverlay.className = "term-cursor"; cursorOverlay.style.display = "none";
  screen.appendChild(sb); screen.appendChild(lv); lv.appendChild(cursorOverlay);
  vt._rowCache = [];
  alloc();

  function pinThreshold() {
    // Generous slack: scrollbar thumb/layout jitter + one–two line heights must
    // not drop the pin while the user is still "at the live prompt".
    return Math.max(120, (vt._cellH || 16) * 5);
  }
  function distFromBottom() {
    return screen.scrollHeight - screen.scrollTop - screen.clientHeight;
  }
  // Programmatic stickScroll fires "scroll" events and browser overflow
  // anchoring can nudge scrollTop when scrollback rows are appended above the
  // live grid. Those must NEVER clear _pinBottom — only real user gestures do.
  let _stickLock = 0;
  let _stickGen = 0;
  let _gestureUntil = 0;
  function stickScroll() {
    if (!vt._pinBottom) return;
    const gen = ++_stickGen;
    _stickLock++;
    const apply = () => {
      if (gen !== _stickGen || !vt._pinBottom) return;
      // Assign twice: some engines clamp against a stale scrollHeight mid-layout.
      screen.scrollTop = screen.scrollHeight;
      screen.scrollTop = screen.scrollHeight;
    };
    apply();
    requestAnimationFrame(() => {
      apply();
      requestAnimationFrame(() => {
        apply();
        _stickLock = Math.max(0, _stickLock - 1);
      });
    });
  }
  vt.followOutput = function () {
    vt._pinBottom = true;
    stickScroll();
  };
  function markUserScrollGesture() {
    // Keep the window open across wheel/trackpad momentum so we don't restick
    // mid-gesture (that was yanking the prompt back into view while reading).
    _gestureUntil = performance.now() + 450;
  }
  screen.addEventListener("wheel", markUserScrollGesture, { passive: true });
  screen.addEventListener("touchstart", markUserScrollGesture, { passive: true });
  screen.addEventListener("touchmove", markUserScrollGesture, { passive: true });
  // Scrollbar thumb / track drag (no wheel events on most engines).
  screen.addEventListener("pointerdown", (ev) => {
    const rect = screen.getBoundingClientRect();
    if (ev.clientX >= rect.right - 20) markUserScrollGesture();
  }, { passive: true });
  screen.addEventListener("scroll", () => {
    if (_stickLock > 0) return;
    if (performance.now() < _gestureUntil) {
      vt._pinBottom = distFromBottom() <= pinThreshold();
      return;
    }
    if (vt._pinBottom) {
      // Layout / overflow-anchor drift while following — pull back, do not unpin.
      if (distFromBottom() > pinThreshold()) stickScroll();
      return;
    }
    // User was reading scrollback; re-pin if they scrolled back to the live end.
    if (distFromBottom() <= pinThreshold()) vt._pinBottom = true;
  }, { passive: true });

  function clearCell(cell) { cell.c = " "; cell.f = null; cell.b = vt.bg; cell.a = 0; }
  function scrollUp(n) {
    for (let i = 0; i < n; i++) {
      const removed = vt.grid.splice(vt.top, 1)[0];
      if (!vt.altActive && vt.top === 0) {
        const div = document.createElement("div"); div.className = "term-row"; div.innerHTML = renderRow(removed, -1);
        sb.appendChild(div);
        while (sb.childElementCount > vt.SB_MAX) sb.removeChild(sb.firstChild);
      }
      vt.grid.splice(vt.bot, 0, newRow());
    }
    // scrollback DOM grows immediately (before paint). Stick now so a large
    // write chunk cannot leave the live area below the fold until rAF render.
    stickScroll();
  }
  function scrollDown(n) { for (let i = 0; i < n; i++) { vt.grid.splice(vt.bot, 1); vt.grid.splice(vt.top, 0, newRow()); } }
  function lineFeed() { if (vt.cy === vt.bot) scrollUp(1); else if (vt.cy < vt.rows - 1) vt.cy++; }
  function revIndex() { if (vt.cy === vt.top) scrollDown(1); else if (vt.cy > 0) vt.cy--; }
  // East-Asian / emoji cell width (0 = skip combining, 2 = wide, else 1).
  function vtCharWidth(cp) {
    if (cp === 0) return 0;
    if (cp < 32 || (cp >= 0x7f && cp < 0xa0)) return 0;
    // Combining marks
    if ((cp >= 0x0300 && cp <= 0x036f) || (cp >= 0x1ab0 && cp <= 0x1aff) ||
        (cp >= 0x1dc0 && cp <= 0x1dff) || (cp >= 0x20d0 && cp <= 0x20ff) ||
        (cp >= 0xfe20 && cp <= 0xfe2f)) return 0;
    // Wide: CJK, fullwidth forms, emoji blocks (simplified wcwidth)
    if ((cp >= 0x1100 && cp <= 0x115f) || (cp >= 0x2329 && cp <= 0x232a) ||
        (cp >= 0x2e80 && cp <= 0xa4cf) || (cp >= 0xac00 && cp <= 0xd7a3) ||
        (cp >= 0xf900 && cp <= 0xfaff) || (cp >= 0xfe10 && cp <= 0xfe19) ||
        (cp >= 0xfe30 && cp <= 0xfe6f) || (cp >= 0xff00 && cp <= 0xff60) ||
        (cp >= 0xffe0 && cp <= 0xffe6) || (cp >= 0x1f300 && cp <= 0x1faff) ||
        (cp >= 0x20000 && cp <= 0x3fffd)) return 2;
    return 1;
  }
  function putChar(ch) {
    const cp = ch.codePointAt(0) || 0;
    const w = vtCharWidth(cp);
    if (w <= 0) return;
    if (vt.wrapNext) { vt.cx = 0; lineFeed(); vt.wrapNext = false; }
    if (vt.cx + w > vt.cols) { vt.cx = 0; lineFeed(); vt.wrapNext = false; }
    const cell = vt.grid[vt.cy][vt.cx];
    cell.c = ch; cell.f = vt.fg; cell.b = vt.bg; cell.a = vt.flags;
    if (w === 2 && vt.cx + 1 < vt.cols) {
      const pad = vt.grid[vt.cy][vt.cx + 1];
      pad.c = ""; pad.f = vt.fg; pad.b = vt.bg; pad.a = vt.flags; // wide-char spacer
    }
    vt.cx += w;
    if (vt.cx >= vt.cols) { vt.cx = vt.cols - 1; vt.wrapNext = true; }
  }
  function eraseInLine(m) {
    const row = vt.grid[vt.cy];
    if (m === 1) { for (let x = 0; x <= vt.cx; x++) clearCell(row[x]); }
    else if (m === 2) { for (let x = 0; x < vt.cols; x++) clearCell(row[x]); }
    else { for (let x = vt.cx; x < vt.cols; x++) clearCell(row[x]); }
  }
  function eraseDisplay(m) {
    if (m === 1) { for (let y = 0; y < vt.cy; y++) for (let x = 0; x < vt.cols; x++) clearCell(vt.grid[y][x]); eraseInLine(1); }
    else if (m === 2 || m === 3) { for (let y = 0; y < vt.rows; y++) for (let x = 0; x < vt.cols; x++) clearCell(vt.grid[y][x]); if (m === 3) sb.innerHTML = ""; }
    else { eraseInLine(0); for (let y = vt.cy + 1; y < vt.rows; y++) for (let x = 0; x < vt.cols; x++) clearCell(vt.grid[y][x]); }
  }
  function saveCursor() { vt.sCx = vt.cx; vt.sCy = vt.cy; vt.sFg = vt.fg; vt.sBg = vt.bg; vt.sFlags = vt.flags; }
  function restoreCursor() { vt.cx = clampX(vt.sCx); vt.cy = clampY(vt.sCy); vt.fg = vt.sFg; vt.bg = vt.sBg; vt.flags = vt.sFlags; }
  function enterAlt() { if (vt.altActive) return; vt.altActive = true; vt.savedGrid = vt.grid; vt.savedPos = { x: vt.cx, y: vt.cy }; alloc(); vt.cx = 0; vt.cy = 0; vt._rowCache = []; sb.style.display = "none"; }
  function exitAlt() { if (!vt.altActive) return; vt.altActive = false; vt.grid = vt.savedGrid; if (vt.savedPos) { vt.cx = clampX(vt.savedPos.x); vt.cy = clampY(vt.savedPos.y); } vt.top = 0; vt.bot = vt.rows - 1; vt._rowCache = []; sb.style.display = ""; }
  function fullReset() { vt.fg = vt.bg = null; vt.flags = 0; vt.top = 0; vt.bot = vt.rows - 1; if (vt.altActive) exitAlt(); alloc(); vt.cx = vt.cy = 0; vt.wrapNext = false; vt._rowCache = []; }

  function sgrExt(ps, i, isFg) {
    const mode = ps[i + 1]; let color = null, used = i;
    if (mode === 5) { color = vt256(ps[i + 2] || 0); used = i + 2; }
    else if (mode === 2) { color = `rgb(${ps[i + 2] || 0},${ps[i + 3] || 0},${ps[i + 4] || 0})`; used = i + 4; }
    if (color !== null) { if (isFg) vt.fg = color; else vt.bg = color; }
    return used;
  }
  function sgr(ps) {
    if (!ps.length) ps = [0];
    for (let i = 0; i < ps.length; i++) {
      const n = ps[i];
      if (n === 0) { vt.fg = vt.bg = null; vt.flags = 0; }
      else if (n === 1) vt.flags |= 1; else if (n === 2) vt.flags |= 8;
      else if (n === 4) vt.flags |= 4; else if (n === 7) vt.flags |= 2;
      else if (n === 22) vt.flags &= ~9; else if (n === 24) vt.flags &= ~4; else if (n === 27) vt.flags &= ~2;
      else if (n >= 30 && n <= 37) vt.fg = VT_PAL[n - 30];
      else if (n === 38) i = sgrExt(ps, i, true);
      else if (n === 39) vt.fg = null;
      else if (n >= 40 && n <= 47) vt.bg = VT_PAL[n - 40];
      else if (n === 48) i = sgrExt(ps, i, false);
      else if (n === 49) vt.bg = null;
      else if (n >= 90 && n <= 97) vt.fg = VT_PAL[8 + n - 90];
      else if (n >= 100 && n <= 107) vt.bg = VT_PAL[8 + n - 100];
    }
  }
  function setMode(ps, priv, on) {
    if (!priv) return;
    for (const n of ps) {
      if (n === 25) vt.cursorVis = on;
      else if (n === 1) vt.appCursor = on;
      else if (n === 47 || n === 1047 || n === 1049) { on ? enterAlt() : exitAlt(); }
    }
  }
  function csi(f) {
    const priv = vt.coll.indexOf("?") >= 0;
    const ps = vt.parm.split(";").map(x => x === "" ? 0 : parseInt(x, 10) || 0);
    const p0 = ps[0] || 0, row = () => vt.grid[vt.cy];
    switch (f) {
      case "A": vt.cy = Math.max(vt.top, vt.cy - Math.max(1, p0)); break;
      case "B": vt.cy = Math.min(vt.bot, vt.cy + Math.max(1, p0)); break;
      case "C": vt.cx = Math.min(vt.cols - 1, vt.cx + Math.max(1, p0)); vt.wrapNext = false; break;
      case "D": vt.cx = Math.max(0, vt.cx - Math.max(1, p0)); vt.wrapNext = false; break;
      case "E": vt.cx = 0; vt.cy = Math.min(vt.bot, vt.cy + Math.max(1, p0)); break;
      case "F": vt.cx = 0; vt.cy = Math.max(vt.top, vt.cy - Math.max(1, p0)); break;
      case "G": case "`": vt.cx = clampX((p0 || 1) - 1); vt.wrapNext = false; break;
      case "d": vt.cy = clampY((p0 || 1) - 1); break;
      case "H": case "f": vt.cy = clampY((ps[0] || 1) - 1); vt.cx = clampX((ps[1] || 1) - 1); vt.wrapNext = false; break;
      case "J": eraseDisplay(p0); break;
      case "K": eraseInLine(p0); break;
      case "m": sgr(ps); break;
      case "r": { const t = (ps[0] || 1) - 1, b = (ps[1] || vt.rows) - 1; if (t < b) { vt.top = clampY(t); vt.bot = clampY(b); vt.cx = 0; vt.cy = vt.top; } break; }
      case "s": saveCursor(); break;
      case "u": restoreCursor(); break;
      case "L": if (vt.cy >= vt.top && vt.cy <= vt.bot) for (let i = 0; i < Math.max(1, p0); i++) { vt.grid.splice(vt.bot, 1); vt.grid.splice(vt.cy, 0, newRow()); } break;
      case "M": if (vt.cy >= vt.top && vt.cy <= vt.bot) for (let i = 0; i < Math.max(1, p0); i++) { vt.grid.splice(vt.cy, 1); vt.grid.splice(vt.bot, 0, newRow()); } break;
      case "P": { const r = row(); for (let i = 0; i < Math.max(1, p0); i++) { r.splice(vt.cx, 1); r.push(blank()); } break; }
      case "@": { const r = row(); for (let i = 0; i < Math.max(1, p0); i++) { r.splice(vt.cx, 0, blank()); r.pop(); } break; }
      case "X": { const r = row(); for (let x = vt.cx; x < Math.min(vt.cols, vt.cx + Math.max(1, p0)); x++) clearCell(r[x]); break; }
      case "S": scrollUp(Math.max(1, p0)); break;
      case "T": scrollDown(Math.max(1, p0)); break;
      case "h": setMode(ps, priv, true); break;
      case "l": setMode(ps, priv, false); break;
    }
  }
  vt.feed = function (text) {
    for (let i = 0; i < text.length; ) {
      const code = text.charCodeAt(i);
      const cp = text.codePointAt(i);
      const ch = (cp > 0xffff) ? String.fromCodePoint(cp) : text[i];
      const adv = (cp > 0xffff) ? 2 : 1;
      if (vt.st === 0) {
        if (code === 0x1b) { vt.st = 1; vt.parm = ""; vt.coll = ""; i += 1; continue; }
        if (ch === "\r") { vt.cx = 0; vt.wrapNext = false; i += 1; continue; }
        // convertEol: bare LF → CR+LF so pipe-mode shells (Win2012 cmd) don't
        // draw staircase prompts. CRLF already sent as \r then \n is unaffected
        // because \r resets cx first.
        if (code === 10 || code === 11 || code === 12) { vt.cx = 0; lineFeed(); vt.wrapNext = false; i += 1; continue; }
        if (code === 8) { vt.cx = Math.max(0, vt.cx - 1); vt.wrapNext = false; i += 1; continue; }
        if (code === 9) { vt.cx = Math.min(vt.cols - 1, vt.cx - (vt.cx % 8) + 8); i += 1; continue; }
        if (code === 7) { i += 1; continue; }
        if (cp >= 32) putChar(ch);
        i += adv;
        continue;
      }
      if (vt.st === 1) {
        if (ch === "[") { vt.st = 2; vt.parm = ""; vt.coll = ""; }
        else if (ch === "]") { vt.st = 3; vt.parm = ""; }
        else if (ch === "(" || ch === ")" || ch === "*" || ch === "+") vt.st = 4;
        else { if (ch === "M") revIndex(); else if (ch === "D") lineFeed(); else if (ch === "E") { vt.cx = 0; lineFeed(); } else if (ch === "7") saveCursor(); else if (ch === "8") restoreCursor(); else if (ch === "c") fullReset(); vt.st = 0; }
        i += 1; continue;
      }
      if (vt.st === 2) {
        if (code >= 0x40 && code <= 0x7e) { csi(ch); vt.st = 0; }
        else if (ch === "?" || ch === ">" || ch === "=" || ch === "!") vt.coll += ch;
        else vt.parm += ch;
        i += 1; continue;
      }
      if (vt.st === 3) {
        if (code === 7) {
          if (typeof vt.onOSC === "function" && vt.parm) vt.onOSC(vt.parm);
          vt.parm = "";
          vt.st = 0;
        } else if (code === 0x1b) vt.st = 5;
        else vt.parm += ch;
        i += 1; continue;
      }
      if (vt.st === 4) { vt.st = 0; i += 1; continue; }
      if (vt.st === 5) { vt.st = 0; i += 1; continue; }
      i += 1;
    }
    scheduleRender();
  };

  function cellStyle(cell) {
    let f = cell.f, b = cell.b; const a = cell.a;
    if (a & 2) { const t = f; f = b || "#05070b"; b = t || "#d6dde8"; }
    let s = "";
    if (f) s += "color:" + f + ";";
    if (b) s += "background:" + b + ";";
    if (a & 1) s += "font-weight:600;";
    if (a & 8) s += "opacity:.7;";
    if (a & 4) s += "text-decoration:underline;";
    return s;
  }
  function renderRow(rowCells, cursorX) {
    let end = -1;
    for (let x = rowCells.length - 1; x >= 0; x--) { const c = rowCells[x]; if (c.c !== " " || c.f || c.b || c.a) { end = x; break; } }
    if (cursorX >= 0 && cursorX > end) end = cursorX;
    let html = "", run = "", style = null;
    const flush = () => { if (run !== "") { html += style ? `<span style="${style}">${vtEsc(run)}</span>` : vtEsc(run); run = ""; } };
    for (let x = 0; x <= end; x++) {
      const cell = rowCells[x];
      if (x === cursorX) { flush(); style = null; html += `<span class="term-cursor">${vtEsc(cell.c === " " ? " " : cell.c)}</span>`; continue; }
      const st = cellStyle(cell);
      if (st !== style) { flush(); style = st; }
      run += cell.c;
    }
    flush();
    return html;
  }
  // Monospace cell size from a full-width probe — NEVER derive from a content
  // row's getBoundingClientRect()/cols (trimmed rows are shorter → left drifts)
  // or cy*firstRowHeight (bold/CJK rows vary → 忽高忽低).
  function refreshCellMetrics() {
    const probe = document.createElement("div");
    probe.className = "term-row";
    probe.textContent = "XXXXXXXXXX";
    probe.style.cssText = "position:absolute;visibility:hidden;left:-9999px;top:0;pointer-events:none";
    lv.appendChild(probe);
    const rect = probe.getBoundingClientRect();
    const w = rect.width;
    const h = probe.offsetHeight || rect.height;
    lv.removeChild(probe);
    if (w > 0) vt._cellW = w / 10;
    if (h > 0) vt._cellH = h;
    return vt._cellW > 0 && vt._cellH > 0;
  }
  function ensureCellMetrics() {
    if (vt._cellW > 0 && vt._cellH > 0) return true;
    return refreshCellMetrics();
  }
  function placeCursorOverlay(show) {
    if (!show) {
      cursorOverlay.style.display = "none";
      return false;
    }
    if (!ensureCellMetrics()) return false;
    let row = lv.children[vt.cy];
    if (!row || row === cursorOverlay) return false;
    // Prefer the live row box — exact Y even if subpixel line heights differ.
    const top = row.offsetTop;
    const height = row.offsetHeight || vt._cellH;
    const curCell = vt.grid[vt.cy] && vt.grid[vt.cy][vt.cx];
    let cells = 1;
    if (curCell && curCell.c) {
      const cp = curCell.c.codePointAt(0);
      if (cp && vtCharWidth(cp) === 2) cells = 2;
    }
    const left = Math.round(vt.cx * vt._cellW);
    const width = Math.max(1, Math.round(vt._cellW * cells));
    cursorOverlay.style.display = "";
    cursorOverlay.style.top = top + "px";
    cursorOverlay.style.left = left + "px";
    cursorOverlay.style.width = width + "px";
    cursorOverlay.style.height = height + "px";
    cursorOverlay.style.lineHeight = height + "px";
    cursorOverlay.textContent = (curCell && curCell.c && curCell.c !== " ") ? curCell.c : " ";
    return true;
  }

  function render() {
    // screen.contains 涵盖两种焦点来源：<pre> 自身聚焦（桌面直接 Tab）
    // 和隐藏 <textarea> 子元素聚焦（移动端虚拟键盘 / 桌面端统一输入入口）
    const focused = screen.contains(document.activeElement);
    const showCursor = vt.cursorVis && focused;

    // 行级缓存：仅更新内容变化的行 DOM，避免全量 innerHTML 替换
    // 先把 cursorOverlay 移到末尾，保证 lv.children[0..rows-1] 都是行元素
    if (cursorOverlay.parentNode === lv) lv.appendChild(cursorOverlay);
    const cache = vt._rowCache;
    for (let y = 0; y < vt.rows; y++) {
      const rowHTML = renderRow(vt.grid[y], -1);
      if (cache[y] === rowHTML) continue;
      cache[y] = rowHTML;
      let el = lv.children[y];
      if (!el || el === cursorOverlay) {
        el = document.createElement("div");
        el.className = "term-row";
        lv.insertBefore(el, cursorOverlay);
      }
      el.innerHTML = rowHTML;
    }
    // 移除多余行（resize 缩小后）
    while (lv.children.length > vt.rows + 1) {
      const last = lv.lastChild;
      if (last === cursorOverlay) { if (lv.children.length > 1) lv.removeChild(lv.children[lv.children.length - 2]); else break; }
      else lv.removeChild(last);
    }
    cache.length = vt.rows;

    // 光标叠层：贴齐目标行 offsetTop + 等宽列宽，严格跟随输入格
    if (!placeCursorOverlay(showCursor) && showCursor) {
      // 度量未就绪时降级为行内光标（仅首帧）
      cursorOverlay.style.display = "none";
      const curRow = lv.children[vt.cy];
      if (curRow && curRow !== cursorOverlay) {
        curRow.innerHTML = renderRow(vt.grid[vt.cy], vt.cx);
        cache[vt.cy] = null;
      }
    }

    // Pin to the live prompt when following; never re-evaluate distance after a
    // burst grew scrollback (that was how the input row vanished with long output).
    // When the user is reading scrollback (_pinBottom=false), leave scrollTop alone.
    if (vt._pinBottom) stickScroll();
  }
  function scheduleRender() {
    if (vt.pending) return;
    vt.pending = true;
    const run = () => { if (!vt.pending) return; vt.pending = false; render(); };
    requestAnimationFrame(run);       // 可见标签页：随帧渲染，流畅
    setTimeout(run, 120);             // 兜底：后台标签页 rAF 被暂停时仍能渲染
  }

  function reshapeGrid(old, cols, rows) {
    const g = [];
    for (let y = 0; y < rows; y++) {
      const r = newRow();
      if (old && old[y]) for (let x = 0; x < Math.min(cols, old[y].length); x++) r[x] = old[y][x];
      g.push(r);
    }
    return g;
  }
  // resizeTo — 重新分配网格到指定 cols/rows，保留已有内容（含 alt 屏下的主缓冲）
  vt.resizeTo = function(cols, rows) {
    cols = Math.max(20, cols); rows = Math.max(6, rows);
    if (cols === vt.cols && rows === vt.rows) return;
    const old = vt.grid;
    vt.cols = cols; vt.rows = rows;
    vt.grid = reshapeGrid(old, cols, rows);
    if (vt.savedGrid) vt.savedGrid = reshapeGrid(vt.savedGrid, cols, rows);
    vt.top = 0; vt.bot = rows - 1; vt.cx = clampX(vt.cx); vt.cy = clampY(vt.cy); vt.wrapNext = false; vt._rowCache = [];
    scheduleRender();
  };

  vt.fit = function () {
    refreshCellMetrics();
    const cw = vt._cellW, chh = vt._cellH;
    if (!cw || !chh) return null;
    const cs = getComputedStyle(screen);
    const padX = parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight);
    const padY = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
    // 1px slack avoids a subpixel-tall last row sitting under the fold / scrollbar.
    const cols = Math.max(20, Math.floor((screen.clientWidth - padX) / cw));
    const rows = Math.max(6, Math.floor((screen.clientHeight - padY - 1) / chh));
    vt.resizeTo(cols, rows);
    // Re-measure after resize (font metrics stable; keep cache fresh for DPI/zoom).
    refreshCellMetrics();
    if (vt._pinBottom) stickScroll();
    return { cols: vt.cols, rows: vt.rows };
  };
  vt.fullReset = fullReset;
  vt.render = render;
  return vt;
}
