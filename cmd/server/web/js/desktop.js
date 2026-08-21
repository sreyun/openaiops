/* ---------- 远程桌面：推流 · 多屏 · 剪贴板 · H264 · 拖拽 · 回放 ---------- */
let DESK_WS = null;
let DESK_HOST = null;
let DESK_META = {
  w: 1920, h: 1080, monitors: [], h264: false, viewOnly: false,
  os: "", desktop: "", secureDesktop: false, inputDesktopOk: true, lockHint: "",
  features: { cad: false, type_text: false, chords: false, wake: false, input: true }
};
let DESK_QUALITY = { quality: 82, fps: 20, sharpness: 1.35, auto_scale: true, codec: "jpeg", monitor: 0 };
let DESK_DOWNLOAD = null;
let DESK_MSE = null; // { mediaSource, sourceBuffer, queue, video, gen }
let DESK_GOT_FRAME = false;
let DESK_PHASE = "idle"; // idle|connecting|waiting_agent|streaming|error|closed
let DESK_UNIFORM_STREAK = 0;
let DESK_SURFACE_MODE = ""; // "canvas" | "video" | ""
let DESK_INTENTIONAL_CLOSE = false;
let DESK_RETRY = 0;
let DESK_MAX_RETRY = 30;
let DESK_NO_RETRY = false; // permission / fatal agent errors — do not reconnect 30×
let DESK_BREAK_GLASS = false; // admin remote-gate override
let DESK_CLIP_AUTOSYNC = false; // auto-write remote clipboard into local OS clipboard
let DESK_LAST_PTR = null; // last mapped remote coords for drag-off mouseup
let _deskHeartbeatWorker = null;
let _deskMSEGen = 0;

function openDesktop(id, name) {
  if (!DESKTOP_ENABLED) { toast(I18N.t("desktop.disabled"), "err"); return; }
  if (TERM_AUTH_CHECKING) return;
  TERM_AUTH_PENDING = { id, name, action: "desktop" };
  checkTerminalAccess();
}

async function doOpenDesktop(id, name) {
  const gate = await confirmRemoteGate(id);
  if (!gate.ok) return;
  const mask = $("desktopMask");
  const title = $("desktopTitle");
  if (title) title.textContent = (name || id) + " · " + I18N.t("desktop.title");
  if (mask) mask.classList.add("show");
  DESK_GOT_FRAME = false;
  DESK_PHASE = "connecting";
  DESK_UNIFORM_STREAK = 0;
  DESK_SURFACE_MODE = "";
  DESK_INTENTIONAL_CLOSE = false;
  DESK_RETRY = 0;
  DESK_NO_RETRY = false;
  DESK_BREAK_GLASS = !!gate.breakGlass;
  DESK_META = {
    w: 1920, h: 1080, monitors: [], h264: false, viewOnly: false,
    os: "", desktop: "", secureDesktop: false, inputDesktopOk: true, lockHint: "",
    features: { cad: false, type_text: false, chords: false, wake: false, input: true }
  };
  DESK_QUALITY = { quality: 82, fps: 20, sharpness: 1.35, auto_scale: true, codec: "jpeg", monitor: 0 };
  DESK_HOST = { id, name };
  renderDesktopShell(id, name);
  const chip = $("deskGateChip");
  if (chip && gate.pf) {
    chip.innerHTML = freezeBadgeHTML(gate.pf) + remoteGateBadgeHTML(gate.pf);
  }
  try {
    const q = DESK_BREAK_GLASS ? "?break_glass=1" : "";
    const r = await fetch(`${API}/hosts/${encodeURIComponent(id)}/desktop${q}`, {
      method: "POST", credentials: "include",
      headers: { "Content-Type": "application/json" }, body: "{}"
    });
    const data = await r.json().catch(() => ({}));
    if (r.status === 403 && data.code === "terminal_verify_required") {
      closeDesktopMask();
      TERM_AUTH_PENDING = { id, name, action: "desktop" };
      TERM_AUTH_VERIFIED = false;
      showTermVerify();
      return;
    }
    if (r.status === 403 && data.code === "remote_gate_required") {
      const retry = await confirmRemoteGate(id);
      if (retry.ok && retry.breakGlass) {
        DESK_BREAK_GLASS = true;
        const r2 = await fetch(`${API}/hosts/${encodeURIComponent(id)}/desktop?break_glass=1`, {
          method: "POST", credentials: "include",
          headers: { "Content-Type": "application/json" }, body: "{}"
        });
        const d2 = await r2.json().catch(() => ({}));
        if (!r2.ok) {
          setDesktopStatus(esc(d2.error || data.error || ""), true);
          setDeskPlaceholder(I18N.t("desktop.error"), d2.error || data.error || "");
          return;
        }
        connectDesktopWS(id, name);
        return;
      }
      setDesktopStatus(esc(data.error || ""), true);
      setDeskPlaceholder(I18N.t("desktop.error"), data.error || "");
      return;
    }
    if (!r.ok) {
      setDesktopStatus(esc(data.error || I18N.t("toast.update_failed2")), true);
      setDeskPlaceholder(I18N.t("desktop.error"), data.error || "");
      return;
    }
    connectDesktopWS(id, name);
  } catch (e) {
    setDesktopStatus(esc(String(e)), true);
    setDeskPlaceholder(I18N.t("desktop.error"), String(e));
  }
}

function renderDesktopShell(id, name) {
  const body = $("desktopBody");
  if (!body) return;
  body.innerHTML = `
    <div class="desk-layout">
      <div class="desk-main">
        <div class="desk-toolbar">
          <div class="desk-status-wrap">
            <span class="desk-dot" id="deskDot"></span>
            <span class="desk-status" id="deskStatus">${esc(I18N.t("desktop.connecting"))}</span>
            <span id="deskGateChip" class="desk-gate-chip"></span>
          </div>
          <div class="desk-tools">
            <label class="desk-q-label"><span>${esc(I18N.t("desktop.monitor"))}</span>
              <select id="deskMonitor" class="desk-select"><option value="0">—</option></select>
            </label>
            <label class="desk-q-label" id="deskResLabel" hidden><span>${esc(I18N.t("desktop.resolution", "分辨率"))}</span>
              <select id="deskResolution" class="desk-select"></select>
            </label>
            <label class="desk-q-label"><span>${esc(I18N.t("desktop.quality"))}</span>
              <select id="deskQuality" class="desk-select">
                <option value="fast">${esc(I18N.t("desktop.q_fast"))}</option>
                <option value="balanced">${esc(I18N.t("desktop.q_balanced"))}</option>
                <option value="clear" selected>${esc(I18N.t("desktop.q_clear"))}</option>
              </select>
            </label>
            <label class="desk-q-label"><span>${esc(I18N.t("desktop.codec"))}</span>
              <select id="deskCodec" class="desk-select">
                <option value="jpeg" selected>JPEG</option>
                <option value="h264">H.264</option>
                <option value="h265">H.265</option>
              </select>
            </label>
            <div class="desk-lock-tools" id="deskLockTools" title="${esc(I18N.t("desktop.lock_tools_hint", "锁屏快捷操作"))}">
              <button type="button" class="btn sm" data-desk-act="cad" id="deskCadBtn">${esc(I18N.t("desktop.cad", "Ctrl+Alt+Del"))}</button>
              <button type="button" class="btn sm" data-desk-act="wake">${esc(I18N.t("desktop.wake", "唤醒"))}</button>
              <button type="button" class="btn sm" data-desk-act="unlock">${esc(I18N.t("desktop.unlock", "解锁"))}</button>
              <button type="button" class="btn sm ghost" data-desk-act="chord" data-desk-chord="esc">Esc</button>
              <button type="button" class="btn sm ghost" data-desk-act="chord" data-desk-chord="win_l">${esc(I18N.t("desktop.win_l", "Win+L"))}</button>
              <button type="button" class="btn sm ghost" data-desk-act="chord" data-desk-chord="ctrl_shift_esc">${esc(I18N.t("desktop.taskmgr", "任务管理器"))}</button>
            </div>
            <button type="button" class="btn sm" id="deskClipSend" title="${esc(I18N.t("desktop.clip_send"))}">📋</button>
            <button type="button" class="btn sm" id="deskFullscreen" title="${esc(I18N.t("desktop.fullscreen"))}">⛶</button>
            <button type="button" class="btn sm" id="deskSessions">${esc(I18N.t("desktop.sessions"))}</button>
            <button type="button" class="btn sm" id="deskDisconnect">${esc(I18N.t("desktop.disconnect"))}</button>
          </div>
        </div>
        <div class="desk-lock-hint" id="deskLockHint" hidden></div>
        <div class="desk-unlock-panel" id="deskUnlockPanel" hidden>
          <div class="desk-unlock-title">${esc(I18N.t("desktop.unlock_title", "发送解锁凭据"))}</div>
          <p class="hint desk-unlock-warn">${esc(I18N.t("desktop.unlock_warn", "仅本次内存发送，不落盘；操作会记入审计（不含明文）。"))}</p>
          <label class="desk-field">${esc(I18N.t("desktop.unlock_user", "用户名（可选）"))}
            <input type="text" id="deskUnlockUser" class="desk-input" autocomplete="off" spellcheck="false">
          </label>
          <label class="desk-field">${esc(I18N.t("desktop.unlock_pass", "密码"))}
            <input type="password" id="deskUnlockPass" class="desk-input" autocomplete="off">
          </label>
          <div class="desk-row" style="gap:8px;margin-top:8px">
            <button type="button" class="btn primary sm" id="deskUnlockSend">${esc(I18N.t("desktop.unlock_send", "发送并回车"))}</button>
            <button type="button" class="btn sm" id="deskUnlockCancel">${esc(I18N.t("ui.cancel", "取消"))}</button>
          </div>
        </div>
        <div class="desk-stage" id="deskStage">
          <div class="desk-placeholder" id="deskPlaceholder">
            <div class="desk-spinner" aria-hidden="true"></div>
            <div class="desk-ph-title" id="deskPhTitle">${esc(I18N.t("desktop.connecting"))}</div>
            <div class="desk-ph-sub" id="deskPhSub">${esc(I18N.t("desktop.wait_hint"))}</div>
          </div>
          <canvas id="deskCanvas" tabindex="0" style="display:none"></canvas>
          <video id="deskVideo" playsinline autoplay muted style="display:none"></video>
          <div class="desk-drop-hint" id="deskDropHint">${esc(I18N.t("desktop.drop_hint"))}</div>
        </div>
      </div>
      <aside class="desk-side" id="deskSide">
        <button type="button" class="desk-side-toggle" id="deskSideToggle" aria-expanded="true">${esc(I18N.t("desktop.side_toggle"))}</button>
        <div class="desk-side-body" id="deskSideBody">
        <div class="desk-side-title">${esc(I18N.t("desktop.files"))}</div>
        <div class="desk-side-hint">${esc(I18N.t("desktop.files_hint"))}</div>
        <label class="desk-field">${esc(I18N.t("desktop.upload_path"))}
          <input type="text" id="deskUploadPath" class="desk-input" placeholder="C:\\Temp\\ 或 /tmp/" autocomplete="off">
        </label>
        <div class="desk-row">
          <input type="file" id="deskFileInput" hidden>
          <button type="button" class="btn primary sm" id="deskUploadBtn">${esc(I18N.t("desktop.upload"))}</button>
        </div>
        <label class="desk-field">${esc(I18N.t("desktop.download_path"))}
          <input type="text" id="deskDownloadPath" class="desk-input" placeholder="${esc(I18N.t("desktop.download_ph"))}" autocomplete="off">
        </label>
        <button type="button" class="btn sm" id="deskDownloadBtn">${esc(I18N.t("desktop.download"))}</button>
        <div class="desk-xfer" id="deskXferLog"></div>
        <div class="desk-side-title" style="margin-top:12px">${esc(I18N.t("desktop.clipboard"))}</div>
        <label class="desk-field" style="flex-direction:row;align-items:center;gap:8px">
          <input type="checkbox" id="deskClipAutoSync" ${DESK_CLIP_AUTOSYNC ? "checked" : ""}/>
          <span>${esc(I18N.t("desktop.clip_autosync"))}</span>
        </label>
        <textarea id="deskClipBox" class="desk-clip" rows="4" placeholder="${esc(I18N.t("desktop.clip_ph"))}"></textarea>
        <button type="button" class="btn sm" id="deskClipApply">${esc(I18N.t("desktop.clip_to_remote"))}</button>
        </div>
      </aside>
    </div>
    <div class="desk-replay" id="deskReplayPane" hidden>
      <div class="desk-replay-bar">
        <span id="deskReplayTitle">${esc(I18N.t("desktop.sessions"))}</span>
        <button type="button" class="btn sm" id="deskReplayClose">${esc(I18N.t("ui.close","关闭"))}</button>
      </div>
      <div id="deskSessionsList" class="desk-sessions-list"></div>
      <canvas id="deskReplayCanvas" style="max-width:100%;background:#000;display:none"></canvas>
    </div>`;
  if (!body.dataset.deskBound) {
    body.dataset.deskBound = "1";
    body.addEventListener("click", onDesktopUIClick);
  }
  // Bind selects every render — innerHTML recreates nodes; do not rely on
  // bubbling alone (fullscreen / some WebKit paths drop delegated change).
  ["deskQuality", "deskCodec", "deskMonitor", "deskResolution", "deskClipAutoSync", "deskFileInput"].forEach((id) => {
    const el = $(id);
    if (!el) return;
    el.onchange = onDesktopUIChange;
  });
  const stage = $("deskStage");
  if (stage) {
    stage.dataset.dnd = "1";
    stage.ondragover = e => { e.preventDefault(); stage.classList.add("drag"); };
    stage.ondragleave = () => stage.classList.remove("drag");
    stage.ondrop = onDeskDrop;
    // Click empty stage chrome to focus the stream surface for immediate typing.
    stage.addEventListener("pointerdown", (ev) => {
      // Ignore toolbar / unlock / side-panel — do not steal focus from credential
      // or clipboard inputs (otherwise local typing appears "dead").
      if (ev.target && ev.target.closest && ev.target.closest(".desk-tools, .desk-side, .desk-unlock-panel, #deskUnlockPanel, select, button, input, textarea, label")) return;
      const canvas = $("deskCanvas");
      const video = $("deskVideo");
      const target = (video && video.style.display !== "none") ? video : canvas;
      if (target) try { target.focus({ preventScroll: true }); } catch (e) { try { target.focus(); } catch (e2) {} }
    });
  }
  DESK_HOST = { id, name };
  setDeskDot("connecting");
  ensureDeskHeartbeatWorker();
  ensureDeskStageResizeObserver();
  // Mobile: collapse the side panel so the stream gets the first viewport.
  try {
    if (window.matchMedia && window.matchMedia("(max-width:900px)").matches) {
      const side = $("deskSide");
      if (side) {
        side.classList.add("is-collapsed");
        const btn = $("deskSideToggle");
        if (btn) btn.setAttribute("aria-expanded", "false");
      }
    }
  } catch (e) {}
  if (!document._deskFsBound) {
    document._deskFsBound = true;
    document.addEventListener("fullscreenchange", onDeskFullscreenChange);
    document.addEventListener("webkitfullscreenchange", onDeskFullscreenChange);
  }
}

function ensureDeskHeartbeatWorker() {
  if (_deskHeartbeatWorker) return;
  try {
    const blob = new Blob([`
      let t=null;
      onmessage=function(e){
        if(e.data==="start"){ if(t) clearInterval(t); t=setInterval(function(){ postMessage("tick"); }, 15000); }
        if(e.data==="stop"){ if(t) clearInterval(t); t=null; }
      };
    `], { type: "application/javascript" });
    _deskHeartbeatWorker = new Worker(URL.createObjectURL(blob));
    _deskHeartbeatWorker.onmessage = () => {
      if (DESK_WS && DESK_WS.readyState === 1) {
        try { DESK_WS.send(new Uint8Array(["P".charCodeAt(0)])); } catch (e) {}
      }
    };
    _deskHeartbeatWorker.postMessage("start");
  } catch (e) {
    // Fallback: throttled in background tabs, better than nothing.
    setInterval(() => {
      if (DESK_WS && DESK_WS.readyState === 1) {
        try { DESK_WS.send(new Uint8Array(["P".charCodeAt(0)])); } catch (err) {}
      }
    }, 15000);
  }
}

function onDeskFullscreenChange() {
  const modal = document.querySelector("#desktopMask .desk-modal");
  const stage = $("deskStage");
  if (!modal) return;
  if (!deskFullscreenElement()) {
    modal.classList.remove("is-max");
    if (stage) stage.classList.remove("is-fullscreen-fallback");
  }
}

function setDeskDot(phase) {
  const dot = $("deskDot");
  if (!dot) return;
  dot.className = "desk-dot " + (phase || "");
}

function setDeskPlaceholder(title, sub) {
  const ph = $("deskPlaceholder");
  const t = $("deskPhTitle");
  const s = $("deskPhSub");
  if (ph) ph.style.display = "";
  if (t) t.textContent = title || "";
  if (s) s.textContent = sub || "";
}

function hideDeskPlaceholder() {
  const ph = $("deskPlaceholder");
  if (ph) ph.style.display = "none";
}

// Surface is CSS-absolutely filled (object-fit:contain). Clear leftover inline
// sizes from older builds that used width:auto fit (those made mouse miss).
function fitDeskSurface(el) {
  if (!el) return;
  el.style.width = "";
  el.style.height = "";
}

function deskActiveSurface() {
  const video = $("deskVideo");
  if (video && video.style.display !== "none") return video;
  return $("deskCanvas");
}

function onDeskStageResize() {
  fitDeskSurface(deskActiveSurface());
  if (DESK_WS && DESK_WS.readyState === 1) {
    clearTimeout(window._deskQResizeTimer);
    window._deskQResizeTimer = setTimeout(() => sendDeskQuality(), 180);
  }
}

function ensureDeskStageResizeObserver() {
  const stage = $("deskStage");
  if (!stage || stage._deskRO) return;
  stage._deskRO = true;
  if (typeof ResizeObserver === "function") {
    const ro = new ResizeObserver(() => onDeskStageResize());
    ro.observe(stage);
  } else {
    window.addEventListener("resize", onDeskStageResize);
  }
}

/** macOS Screen Recording / capture permission — reconnecting only re-prompts TCC. */
function deskLooksPermissionError(msg) {
  const s = String(msg || "").toLowerCase();
  return s.includes("desk_perm_denied")
    || s.includes("screen recording")
    || s.includes("录屏")
    || s.includes("screencapture failed")
    || s.includes("privacy & security")
    || s.includes("not authorized");
}

function setDesktopStatus(msg, isErr) {
  const el = $("deskStatus");
  if (!el) return;
  el.textContent = msg;
  el.classList.toggle("err", !!isErr);
  if (isErr) setDeskDot("error");
}

function qualityPreset(name) {
  // Encode scale is auto-matched to the browser stage (client_w/h × dpr × sharpness).
  if (name === "fast") return { quality: 68, fps: 24, sharpness: 1.0, auto_scale: true };
  if (name === "clear") return { quality: 90, fps: 16, sharpness: 1.6, auto_scale: true };
  return { quality: 82, fps: 20, sharpness: 1.35, auto_scale: true };
}

function readDeskClientSize() {
  const stage = $("deskStage");
  const dpr = Math.min(2.5, Math.max(1, window.devicePixelRatio || 1));
  if (!stage) return { client_w: 1280, client_h: 720, dpr };
  const r = stage.getBoundingClientRect();
  return {
    client_w: Math.max(320, Math.round(r.width || stage.clientWidth || 1280)),
    client_h: Math.max(200, Math.round(r.height || stage.clientHeight || 720)),
    dpr
  };
}

function probeDeskClientCodecs() {
  const codecs = [];
  let mseH264 = 'video/mp4; codecs="avc1.42E01E"';
  let mseH265 = 'video/mp4; codecs="hvc1.1.6.L93.B0"';
  if (typeof MediaSource === "undefined" || !MediaSource.isTypeSupported) {
    return { codecs, mseH264, mseH265 };
  }
  if (MediaSource.isTypeSupported(mseH264) || MediaSource.isTypeSupported('video/mp4; codecs="avc1.4D401F"')) {
    codecs.push("h264");
  }
  const h265Try = [
    'video/mp4; codecs="hvc1.1.6.L93.B0"',
    'video/mp4; codecs="hev1.1.6.L93.B0"',
  ];
  for (const c of h265Try) {
    if (MediaSource.isTypeSupported(c)) {
      codecs.push("h265");
      mseH265 = c;
      break;
    }
  }
  return { codecs, mseH264, mseH265 };
}
const DESK_CLIENT_CODEC = probeDeskClientCodecs();

function sendDeskQuality() {
  if (!DESK_WS || DESK_WS.readyState !== 1) return;
  const client = readDeskClientSize();
  const payloadObj = {
    ...DESK_QUALITY,
    auto_scale: true,
    client_w: client.client_w,
    client_h: client.client_h,
    dpr: client.dpr,
    client_codecs: DESK_CLIENT_CODEC.codecs,
    // 声明支持脏块差分帧。Agent 只有看到这一位才会发 'T'——老版控制台不认识它，
    // 发过去就是整屏黑。
    tiles: true
  };
  // Prefer Agent auto-scale; drop legacy fixed scale so it doesn't fight viewport fit.
  delete payloadObj.scale;
  const payload = new TextEncoder().encode(JSON.stringify(payloadObj));
  const buf = new Uint8Array(1 + payload.length);
  buf[0] = "Q".charCodeAt(0);
  buf.set(payload, 1);
  DESK_WS.send(buf);
}

/**
 * drawDeskTiles 画一帧脏块差分（'T'）。
 *
 * 载荷是紧凑二进制（见 Agent 侧 encodeDeskTiles）：
 *   u16 frameW | u16 frameH | u16 count
 *   count × ( u16 x | u16 y | u16 w | u16 h | u32 len | len 字节 JPEG )
 *
 * 差分帧只有在画布尺寸与帧头一致时才能画——尺寸对不上说明客户端手里的底图已经不是
 * Agent 以为的那一张，硬画会画出错位的鬼影。这种情况直接忽略，等下一张整帧关键帧
 * （Agent 至少每 5 秒发一张）。
 */
function drawDeskTiles(canvas, payload, onPainted) {
  if (!canvas || !payload || payload.byteLength < 6) return;
  const dv = new DataView(payload.buffer, payload.byteOffset, payload.byteLength);
  const fw = dv.getUint16(0), fh = dv.getUint16(2), count = dv.getUint16(4);
  if (!fw || !fh || !count) return;
  const ctx = canvas.getContext("2d");
  if (!ctx) return;
  if (canvas.width !== fw || canvas.height !== fh) {
    // 还没有底图（或底图尺寸变了）：等整帧，别画错位的块。
    return;
  }
  let off = 6;
  const jobs = [];
  for (let i = 0; i < count && off + 12 <= payload.byteLength; i++) {
    const x = dv.getUint16(off), y = dv.getUint16(off + 2);
    const w = dv.getUint16(off + 4), h = dv.getUint16(off + 6);
    const len = dv.getUint32(off + 8);
    off += 12;
    if (len <= 0 || off + len > payload.byteLength) break;
    const bytes = payload.slice(off, off + len);
    off += len;
    jobs.push({ x, y, w, h, blob: new Blob([bytes], { type: "image/jpeg" }) });
  }
  if (!jobs.length) return;
  const paintAll = (sources) => {
    for (let i = 0; i < sources.length; i++) {
      const src = sources[i];
      if (!src) continue;
      const j = jobs[i];
      ctx.drawImage(src, j.x, j.y, j.w, j.h);
      if (typeof src.close === "function") { try { src.close(); } catch (e) {} }
    }
    if (typeof onPainted === "function") onPainted();
  };
  if (typeof createImageBitmap === "function") {
    Promise.all(jobs.map(j => createImageBitmap(j.blob).catch(() => null))).then(paintAll);
    return;
  }
  // 老内核兜底：Image + object URL。
  let left = jobs.length;
  const imgs = new Array(jobs.length).fill(null);
  jobs.forEach((j, i) => {
    const url = URL.createObjectURL(j.blob);
    const img = new Image();
    img.onload = () => { URL.revokeObjectURL(url); imgs[i] = img; if (--left === 0) paintAll(imgs); };
    img.onerror = () => { URL.revokeObjectURL(url); if (--left === 0) paintAll(imgs); };
    img.src = url;
  });
}

/**
 * fillDeskResolutions 用 Agent 报上来的可用显示模式填充下拉。
 *
 * 只有平台真的支持改分辨率时 Agent 才会带 modes，所以这个入口默认是隐藏的——
 * 给一个点了必然失败的开关比没有更糟。
 */
function fillDeskResolutions(modes) {
  const sel = $("deskResolution");
  const label = $("deskResLabel");
  if (!sel || !label) return;
  DESK_META.modes = modes;
  const cur = (DESK_META.w && DESK_META.h) ? `${DESK_META.w}x${DESK_META.h}` : "";
  sel.innerHTML =
    `<option value="">${esc(I18N.t("desktop.res_keep", "保持远端不变"))}</option>` +
    `<option value="fit">${esc(I18N.t("desktop.res_fit", "匹配我的窗口"))}</option>` +
    modes.map(m => `<option value="${m.w}x${m.h}"${cur === `${m.w}x${m.h}` ? " selected" : ""}>${m.w}×${m.h}</option>`).join("");
  label.hidden = false;
}

function fillMonitorSelect(mons) {
  const sel = $("deskMonitor");
  if (!sel) return;
  sel.innerHTML = (mons || []).map(m =>
    `<option value="${m.id}">${esc(m.name || ("#" + m.id))} (${m.width}×${m.height})${m.primary ? " ★" : ""}</option>`
  ).join("") || `<option value="0">—</option>`;
  if (mons && mons.length && (!DESK_QUALITY.monitor || !mons.some(m => m.id === DESK_QUALITY.monitor))) {
    const p = mons.find(m => m.primary) || mons[0];
    DESK_QUALITY.monitor = p.id;
  }
  sel.value = String(DESK_QUALITY.monitor || (mons && mons[0] && mons[0].id) || 0);
}

function connectDesktopWS(id, name) {
  closeDesktopWS();
  DESK_GOT_FRAME = false;
  DESK_UNIFORM_STREAK = 0;
  DESK_SURFACE_MODE = "";
  DESK_PHASE = "waiting_agent";
  setDesktopStatus(I18N.t("desktop.waiting_agent"), false);
  setDeskPlaceholder(I18N.t("desktop.waiting_agent"), I18N.t("desktop.wait_hint"));
  setDeskDot("waiting");
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const bg = DESK_BREAK_GLASS ? "?break_glass=1" : "";
  const ws = new WebSocket(`${proto}//${location.host}/api/v1/hosts/${encodeURIComponent(id)}/desktop/ws${bg}`);
  ws.binaryType = "arraybuffer";
  DESK_WS = ws;
  const canvas = $("deskCanvas");
  // Decode one JPEG at a time and retain only the newest waiting frame. The old
  // implementation shared one object URL across every in-flight Image; a newer
  // frame revoked the URL still being decoded, while an older onload could revoke
  // the newer URL. At normal frame rates that race left the canvas at its default
  // 300×150 black surface even though the UI reported "Connected".
  // Prefer createImageBitmap(Blob) so decode does not depend on blob: URLs
  // (a CSP img-src without blob: previously blocked every frame and surfaced as
  // "无法解码的 JPEG 画面"). Fall back to Image + object URL when unavailable.
  let jpegPending = null;
  let jpegDecoding = false;
  let jpegDecodeFailures = 0;

  const drawNextJPEG = () => {
    if (jpegDecoding || !jpegPending || DESK_WS !== ws) return;
    const blob = jpegPending;
    jpegPending = null;
    jpegDecoding = true;
    const finish = () => {
      jpegDecoding = false;
      if (jpegPending && DESK_WS === ws) drawNextJPEG();
    };
    const fail = () => {
      jpegDecodeFailures++;
      if (jpegDecodeFailures === 3 && DESK_WS === ws) {
        DESK_PHASE = "error";
        setDesktopStatus(I18N.t("desktop.jpeg_decode_failed"), true);
        setDeskPlaceholder(I18N.t("desktop.error"), I18N.t("desktop.jpeg_decode_failed"));
        setDeskDot("error");
      }
      finish();
    };
    const paint = (src, w, h) => {
      const ctx = canvas && canvas.getContext("2d");
      if (ctx && w > 0 && h > 0 && DESK_WS === ws) {
        // Ignore 1–2px encoder jitter — resetting canvas.width clears pixels and
        // flashes black between frames (especially visible on Win2012 GDI JPEG).
        if (Math.abs(canvas.width - w) > 2 || Math.abs(canvas.height - h) > 2) {
          canvas.width = w;
          canvas.height = h;
        }
        ctx.drawImage(src, 0, 0, canvas.width, canvas.height);
        if (typeof src.close === "function") {
          try { src.close(); } catch (e) {}
        }
        jpegDecodeFailures = 0;
        const firstFrame = !DESK_GOT_FRAME;
        markDeskStreaming();
        if (firstFrame) {
          showDeskCanvas(true);
          fitDeskSurface(canvas);
        } else if (canvas.style.display === "none") {
          showDeskCanvas(true);
        }
        // Solid-frame guard with hysteresis — toggling the placeholder every
        // borderline JPEG frame looked like continuous screen flicker.
        if (deskOnSecureDesktop()) {
          DESK_UNIFORM_STREAK = 0;
          hideDeskPlaceholder();
          setDeskDot("on");
        } else if (deskCanvasLooksUniform(ctx, canvas.width, canvas.height)) {
          DESK_UNIFORM_STREAK = (DESK_UNIFORM_STREAK || 0) + 1;
          if (DESK_UNIFORM_STREAK >= 8) {
            setDeskPlaceholder(I18N.t("desktop.warn"), I18N.t("desktop.solid_frame_hint"));
            setDeskDot("warn");
          }
        } else {
          DESK_UNIFORM_STREAK = 0;
          hideDeskPlaceholder();
          setDeskDot("on");
        }
      }
      finish();
    };
    if (typeof createImageBitmap === "function") {
      createImageBitmap(blob).then((bmp) => paint(bmp, bmp.width, bmp.height), fail);
      return;
    }
    const frameURL = URL.createObjectURL(blob);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(frameURL);
      paint(img, img.naturalWidth, img.naturalHeight);
    };
    img.onerror = () => {
      URL.revokeObjectURL(frameURL);
      fail();
    };
    img.src = frameURL;
  };

  ws.onopen = () => {
    DESK_RETRY = 0;
    setDesktopStatus(I18N.t("desktop.waiting_agent"), false);
    sendDeskQuality();
    bindDeskSessionKeys();
  };
  ws.onclose = () => {
    const prev = DESK_PHASE;
    DESK_PHASE = "closed";
    closeDeskMSE();
    unbindDesktopInput($("deskStage"));
    unbindDesktopInput(canvas);
    if (DESK_INTENTIONAL_CLOSE) {
      setDesktopStatus(I18N.t("desktop.disconnected"), true);
      setDeskDot("error");
      return;
    }
    // Auto-reconnect with backoff — but never spin on Screen Recording / fatal
    // permission errors (each attempt can re-trigger macOS TCC dialogs).
    if (DESK_NO_RETRY || (prev === "error" && deskLooksPermissionError(DESK_META && DESK_META.lastError))) {
      DESK_NO_RETRY = true;
      setDesktopStatus(I18N.t("desktop.disconnected"), true);
      setDeskDot("error");
      return;
    }
    if (DESK_HOST && DESK_RETRY < DESK_MAX_RETRY) {
      DESK_RETRY++;
      const delay = Math.min(15000, 800 * Math.pow(1.35, DESK_RETRY));
      setDesktopStatus(`${I18N.t("misc.reconnecting")}(${DESK_RETRY}/${DESK_MAX_RETRY})`, false);
      setDeskDot("waiting");
      setDeskPlaceholder(I18N.t("misc.reconnecting"), I18N.t("desktop.wait_hint"));
      setTimeout(() => {
        if (DESK_INTENTIONAL_CLOSE || DESK_NO_RETRY || !DESK_HOST) return;
        const mask = $("desktopMask");
        if (!mask || !mask.classList.contains("show")) return;
        connectDesktopWS(DESK_HOST.id, DESK_HOST.name);
      }, delay);
      return;
    }
    if (prev !== "error" && prev !== "streaming") {
      setDesktopStatus(I18N.t("desktop.disconnected"), true);
      if (!DESK_GOT_FRAME) {
        setDeskPlaceholder(I18N.t("desktop.disconnected"), I18N.t("desktop.wait_hint"));
      }
    } else if (prev === "streaming") {
      setDesktopStatus(I18N.t("desktop.disconnected"), true);
    }
    setDeskDot("error");
  };
  ws.onerror = () => {
    DESK_PHASE = "error";
    setDesktopStatus(I18N.t("desktop.error"), true);
    setDeskDot("error");
    setDeskPlaceholder(I18N.t("desktop.error"), I18N.t("desktop.wait_hint"));
  };
  ws.onmessage = (ev) => {
    if (!(ev.data instanceof ArrayBuffer) || ev.data.byteLength < 1) return;
    const u8 = new Uint8Array(ev.data);
    const typ = String.fromCharCode(u8[0]);
    const payload = u8.subarray(1);
    if (typ === "S") {
      try {
        const meta = JSON.parse(new TextDecoder().decode(payload));
        if (meta.phase === "waiting_agent") {
          DESK_PHASE = "waiting_agent";
          setDesktopStatus(I18N.t("desktop.waiting_agent"), false);
          setDeskPlaceholder(I18N.t("desktop.waiting_agent"), I18N.t("desktop.wait_hint"));
          setDeskDot("waiting");
          return;
        }
        if (meta.phase === "agent_reconnecting") {
          // Windows 登录 / 注销 / 切换用户会让服务在新会话里重开桌面 worker。
          // 会话没断，只是换了一头——告诉用户"正在恢复"，别让人以为要重连。
          DESK_PHASE = "connecting";
          setDesktopStatus(I18N.t("desktop.agent_reconnecting", "远程会话切换中，正在恢复…"), false);
          setDeskDot("waiting");
          return;
        }
        if (meta.phase === "agent_up") {
          if (!DESK_GOT_FRAME) {
            setDesktopStatus(I18N.t("desktop.agent_up"), false);
            setDeskPlaceholder(I18N.t("desktop.agent_up"), I18N.t("desktop.streaming_hint"));
            setDeskDot("waiting");
          }
          // 新接管的 worker 从默认参数起步：把画质/视口/能力位再报一次，
          // 否则它不知道浏览器认识差分帧，会退回整屏推流。
          sendDeskQuality();
        }
        if (meta.w) DESK_META.w = meta.w;
        if (meta.h) DESK_META.h = meta.h;
        if (meta.h264 != null) DESK_META.h264 = !!meta.h264;
        if (meta.clipboard != null || (meta.features && meta.features.clipboard != null)) {
          const clipOK = meta.clipboard != null ? !!meta.clipboard : !!(meta.features && meta.features.clipboard);
          const clipBox = $("deskClipBox");
          const clipApply = $("deskClipApply");
          const clipSend = $("deskClipSend");
          const clipAuto = $("deskClipAutoSync");
          [clipBox, clipApply, clipSend, clipAuto].forEach(el => {
            if (el) el.disabled = !clipOK;
          });
        }
        if (meta.quality_ack) {
          clearTimeout(window._deskQToastTimer);
          const qLabel = ($("deskQuality") && $("deskQuality").selectedOptions[0])
            ? $("deskQuality").selectedOptions[0].text
            : "";
          const enc = meta.encode_scale || meta.scale || 0;
          const detail = [
            enc ? (Math.round(enc * 100) + "%") : "",
            meta.quality ? ("q" + meta.quality) : "",
            meta.fps ? (meta.fps + "fps") : "",
            (meta.client_w && meta.client_h) ? (meta.client_w + "×" + meta.client_h) : ""
          ].filter(Boolean).join(" · ");
          toast(I18N.t("desktop.quality_applied") + (qLabel ? ": " + qLabel : "") + (detail ? ` (${detail})` : ""), "ok");
          setDesktopStatus(I18N.t("desktop.connected") + (detail ? " · " + detail : ""), false);
        }
        if (Array.isArray(meta.modes) && meta.modes.length) fillDeskResolutions(meta.modes);
        if (meta.resolution && meta.resolution.w) {
          toast(I18N.t("desktop.resolution_applied", "已切换远端分辨率") + `：${meta.resolution.w}×${meta.resolution.h}`, "ok");
        }
        if (meta.view_only != null) DESK_META.viewOnly = !!meta.view_only;
        applyDeskInputMeta(meta);
        if (meta.action_ack) {
          if (meta.ok === false) toast(meta.error || I18N.t("desktop.action_fail", "远程动作失败"), "err");
          else if (meta.action === "cad") toast(I18N.t("desktop.cad_sent", "已发送 Ctrl+Alt+Del"), "ok");
          else if (meta.action === "type_text" || meta.action === "unlock") toast(I18N.t("desktop.unlock_sent", "凭据已发送"), "ok");
          else if (meta.action === "paste") toast(I18N.t("desktop.paste_sent", "已粘贴到远程"), "ok");
          else if (meta.action === "wake") toast(I18N.t("desktop.wake_sent", "已尝试唤醒输入框"), "ok");
        }
        if (Array.isArray(meta.monitors)) {
          DESK_META.monitors = meta.monitors;
          fillMonitorSelect(meta.monitors);
        }
        const codecSel = $("deskCodec");
        if (codecSel) {
          const h264opt = codecSel.querySelector('option[value="h264"]');
          const h265opt = codecSel.querySelector('option[value="h265"]');
          if (meta.h264 === false) {
            if (DESK_QUALITY.codec === "h264") {
              DESK_QUALITY.codec = (meta.h265 && DESK_CLIENT_CODEC.codecs.indexOf("h265") >= 0) ? "h265" : "jpeg";
              codecSel.value = DESK_QUALITY.codec;
            }
            if (h264opt) h264opt.disabled = true;
          } else if (h264opt) {
            h264opt.disabled = false;
          }
          if (meta.h265 === false) {
            if (DESK_QUALITY.codec === "h265") {
              DESK_QUALITY.codec = meta.h264 !== false ? "h264" : "jpeg";
              codecSel.value = DESK_QUALITY.codec;
            }
            if (h265opt) h265opt.disabled = true;
          } else if (h265opt) {
            h265opt.disabled = DESK_CLIENT_CODEC.codecs.indexOf("h265") < 0;
          }
        }
        if (!DESK_GOT_FRAME && meta.prefer) {
          if (meta.prefer === "h265" && meta.h265 && DESK_CLIENT_CODEC.codecs.indexOf("h265") >= 0 && DESK_QUALITY.codec !== "h265") {
            DESK_QUALITY.codec = "h265";
            if (codecSel) codecSel.value = "h265";
            sendDeskQuality();
          } else if (meta.prefer === "h264" && meta.h264 && DESK_QUALITY.codec !== "h264") {
            DESK_QUALITY.codec = "h264";
            if (codecSel) codecSel.value = "h264";
            sendDeskQuality();
          }
        }
        if (meta.error) {
          DESK_PHASE = "error";
          setDesktopStatus(meta.error, true);
          setDeskPlaceholder(I18N.t("desktop.error"), meta.error);
          setDeskDot("error");
        }
        if (DESK_PHASE !== "error" && (DESK_META.viewOnly || meta.lock_hint || meta.desktop || meta.action_ack)) {
          refreshDeskInputStatus();
        }
      } catch (e) {}
      return;
    }
    if (typ === "K" && canvas) {
      // Sparse JPEG keyframes may arrive while live-viewing H.264 (for replay).
      // Don't interrupt the video surface once H.264 is showing.
      if ((DESK_QUALITY.codec === "h264" || DESK_QUALITY.codec === "h265") && $("deskVideo") && $("deskVideo").style.display !== "none") {
        return;
      }
      jpegPending = new Blob([payload.slice()], { type: "image/jpeg" });
      drawNextJPEG();
      return;
    }
    if (typ === "T" && canvas) {
      // 脏块差分帧：只画变化的那几块。
      if ((DESK_QUALITY.codec === "h264" || DESK_QUALITY.codec === "h265") && $("deskVideo") && $("deskVideo").style.display !== "none") {
        return;
      }
      drawDeskTiles(canvas, payload, () => {
        const firstFrame = !DESK_GOT_FRAME;
        markDeskStreaming();
        if (firstFrame) { showDeskCanvas(true); fitDeskSurface(canvas); }
        else if (canvas.style.display === "none") showDeskCanvas(true);
        DESK_UNIFORM_STREAK = 0;
        hideDeskPlaceholder();
        setDeskDot("on");
      });
      return;
    }
    if (typ === "P") {
      // keepalive pong — ignore
      return;
    }
    if (typ === "H") {
      const first = !DESK_GOT_FRAME;
      markDeskStreaming();
      if (first || DESK_SURFACE_MODE !== "video") showDeskCanvas(false);
      appendDeskH264(payload);
      return;
    }
    if (typ === "C") {
      try {
        const j = JSON.parse(new TextDecoder().decode(payload));
        if (j.text != null) {
          const box = $("deskClipBox");
          if (box) box.value = j.text;
          if (DESK_CLIP_AUTOSYNC && j.text && navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(j.text).catch(() => {});
          }
        }
      } catch (e) {}
      return;
    }
    if (typ === "F") { handleDeskFileInfo(payload); return; }
    if (typ === "D") { if (DESK_DOWNLOAD) DESK_DOWNLOAD.chunks.push(payload.slice(0)); return; }
    if (typ === "E") {
      if (payload.length > 0) {
        try {
          const j = JSON.parse(new TextDecoder().decode(payload));
          if (j.error) {
            const isWarn = j.level === "warn";
            DESK_META.lastError = j.error;
            setDesktopStatus(j.error, !isWarn);
            // Warn diagnostics (blank capture / no_frame watchdog) must still be
            // visible on the canvas — otherwise a black JPEG stream looks like a
            // successful connection with no explanation.
            if (!DESK_GOT_FRAME || isWarn) {
              setDeskPlaceholder(isWarn ? I18N.t("desktop.warn") : I18N.t("desktop.error"), j.error);
            }
            if (!isWarn) {
              setDeskDot("error");
              DESK_PHASE = "error";
              if (deskLooksPermissionError(j.error)) {
                DESK_NO_RETRY = true;
                DESK_INTENTIONAL_CLOSE = true; // stop WS retry storm / TCC spam
              }
            } else {
              setDeskDot("warn");
            }
            return;
          }
        } catch (e) {
          const msg = new TextDecoder().decode(payload);
          DESK_META.lastError = msg;
          DESK_PHASE = "error";
          setDesktopStatus(msg, true);
          setDeskDot("error");
          if (deskLooksPermissionError(msg)) {
            DESK_NO_RETRY = true;
            DESK_INTENTIONAL_CLOSE = true;
          }
          if (!DESK_GOT_FRAME) setDeskPlaceholder(I18N.t("desktop.error"), msg);
          return;
        }
      }
      if (DESK_DOWNLOAD) finishDeskDownload();
    }
  };
}

function markDeskStreaming() {
  const first = !DESK_GOT_FRAME;
  if (first) {
    DESK_GOT_FRAME = true;
    DESK_PHASE = "streaming";
    DESK_UNIFORM_STREAK = 0;
    hideDeskPlaceholder();
    setDeskDot("on");
    refreshDeskInputStatus();
  }
  const stage = $("deskStage");
  if (stage) bindDesktopInput(stage);
  // Focus / session-key bind only on the first frame. Re-focusing 10–15×/s made
  // the viewport flash (Win2012 JPEG path) and stole focus from toolbar inputs.
  if (!first) {
    bindDeskSessionKeys();
    return;
  }
  const canvas = $("deskCanvas");
  const video = $("deskVideo");
  const useVideo = video && video.style.display !== "none";
  const target = useVideo ? video : canvas;
  if (target) {
    if (!target.hasAttribute("tabindex")) target.setAttribute("tabindex", "0");
    try { target.focus({ preventScroll: true }); } catch (e) { try { target.focus(); } catch (e2) {} }
  }
  bindDeskSessionKeys();
}

function showDeskCanvas(useCanvas) {
  const canvas = $("deskCanvas");
  const video = $("deskVideo");
  const prev = DESK_SURFACE_MODE; // "canvas" | "video" | ""
  const next = useCanvas ? "canvas" : "video";
  if (canvas) canvas.style.display = useCanvas ? "block" : "none";
  if (video) video.style.display = useCanvas ? "none" : "block";
  DESK_SURFACE_MODE = next;
  ensureDeskStageResizeObserver();
  fitDeskSurface(useCanvas ? canvas : video);
  // Bind to the stage so the full viewport receives pointer events (letterbox
  // areas of object-fit:contain still map via deskNormXY).
  const stage = $("deskStage");
  if (stage) bindDesktopInput(stage);
  // Only steal focus when the visible surface actually switches.
  if (prev === next) return;
  const surf = useCanvas ? canvas : video;
  if (surf) {
    if (!surf.hasAttribute("tabindex")) surf.setAttribute("tabindex", "0");
    try { surf.focus({ preventScroll: true }); } catch (e) { try { surf.focus(); } catch (e2) {} }
  }
}

function deskOnSecureDesktop() {
  if (DESK_META && DESK_META.secureDesktop) return true;
  const d = String((DESK_META && DESK_META.desktop) || "").toLowerCase();
  return d === "winlogon" || d.indexOf("winlogon") === 0 || d === "screensaver";
}

// Sparse multi-region sample: true when the painted frame is essentially one
// solid color (disconnected console / wrong desktop / empty Session-0 capture).
// Must NOT only sample the top-left — Winlogon lock chrome often lives elsewhere.
function deskCanvasLooksUniform(ctx, w, h) {
  try {
    if (w < 8 || h < 8) return false;
    const rw = Math.min(40, w);
    const rh = Math.min(40, h);
    const origins = [
      [0, 0],
      [Math.max(0, w - rw), 0],
      [0, Math.max(0, h - rh)],
      [Math.max(0, w - rw), Math.max(0, h - rh)],
      [Math.max(0, Math.floor((w - rw) / 2)), Math.max(0, Math.floor((h - rh) / 2))]
    ];
    let minR = 255, minG = 255, minB = 255, maxR = 0, maxG = 0, maxB = 0, n = 0;
    for (const [ox, oy] of origins) {
      const data = ctx.getImageData(ox, oy, rw, rh).data;
      for (let i = 0; i < data.length; i += 32) {
        const r = data[i], g = data[i + 1], b = data[i + 2];
        if (r < minR) minR = r; if (g < minG) minG = g; if (b < minB) minB = b;
        if (r > maxR) maxR = r; if (g > maxG) maxG = g; if (b > maxB) maxB = b;
        n++;
      }
    }
    return n > 8 && (maxR - minR) <= 10 && (maxG - minG) <= 10 && (maxB - minB) <= 10;
  } catch (e) {
    return false;
  }
}

function closeDeskMSE() {
  _deskMSEGen++;
  if (DESK_MSE && DESK_MSE.mediaSource && DESK_MSE.mediaSource.readyState === "open") {
    try { DESK_MSE.mediaSource.endOfStream(); } catch (e) {}
  }
  DESK_MSE = null;
  const video = $("deskVideo");
  if (video) { video.removeAttribute("src"); video.load(); }
}

function fallBackDeskToJPEG(reason) {
  closeDeskMSE();
  DESK_QUALITY.codec = "jpeg";
  const cs = $("deskCodec"); if (cs) cs.value = "jpeg";
  sendDeskQuality();
  showDeskCanvas(true);
  if (reason) setDesktopStatus(I18N.t("desktop.h264_unsupported") + (reason ? ": " + reason : ""), true);
}

function appendDeskH264(chunk) {
  const video = $("deskVideo");
  if (!video || typeof MediaSource === "undefined") {
    fallBackDeskToJPEG("MediaSource");
    return;
  }
  if (!DESK_MSE) {
    const gen = ++_deskMSEGen;
    const ms = new MediaSource();
    DESK_MSE = { mediaSource: ms, sourceBuffer: null, queue: [], video, gen };
    video.src = URL.createObjectURL(ms);
    ms.addEventListener("sourceopen", () => {
      if (!DESK_MSE || DESK_MSE.gen !== gen) return;
      try {
        const sb = ms.addSourceBuffer(
          DESK_QUALITY.codec === "h265" ? DESK_CLIENT_CODEC.mseH265 : DESK_CLIENT_CODEC.mseH264
        );
        DESK_MSE.sourceBuffer = sb;
        sb.mode = "sequence";
        sb.addEventListener("updateend", flushDeskMSE);
        flushDeskMSE();
      } catch (e) {
        fallBackDeskToJPEG(String(e && e.message || e));
      }
    });
  }
  DESK_MSE.queue.push(chunk.buffer.slice(chunk.byteOffset, chunk.byteOffset + chunk.byteLength));
  // Cap queue to avoid unbounded memory if decode stalls.
  if (DESK_MSE.queue.length > 120) DESK_MSE.queue.splice(0, DESK_MSE.queue.length - 60);
  flushDeskMSE();
}

function flushDeskMSE() {
  const m = DESK_MSE;
  if (!m || !m.sourceBuffer || m.sourceBuffer.updating || !m.queue.length) return;
  try {
    m.sourceBuffer.appendBuffer(m.queue.shift());
  } catch (e) {
    m.queue = [];
    fallBackDeskToJPEG(String(e && e.name || e));
  }
}

function handleDeskFileInfo(payload) {
  let meta = {};
  try { meta = JSON.parse(new TextDecoder().decode(payload)); } catch (e) { return; }
  const log = $("deskXferLog");
  if (meta.type === "upload_ack") {
    const ok = meta.status === "ok";
    toast((ok ? I18N.t("desktop.upload_ok") : I18N.t("desktop.upload_fail")) + (meta.filename ? ": " + meta.filename : "") + (meta.message ? " — " + meta.message : ""), ok ? "ok" : "err");
    if (log) log.textContent = (ok ? "↑ OK " : "↑ ERR ") + (meta.filename || "");
    return;
  }
  if (meta.type === "download_meta" || meta.type === "download_start") {
    DESK_DOWNLOAD = { filename: meta.filename || "download.bin", size: meta.size || 0, chunks: [] };
    toast(I18N.t("desktop.downloading") + ": " + DESK_DOWNLOAD.filename, "info");
    if (log) log.textContent = "↓ " + DESK_DOWNLOAD.filename;
    return;
  }
  if (meta.type === "download_error") {
    toast(I18N.t("desktop.download_fail") + (meta.message ? ": " + meta.message : ""), "err");
    DESK_DOWNLOAD = null;
  }
}

function finishDeskDownload() {
  const dl = DESK_DOWNLOAD; DESK_DOWNLOAD = null;
  if (!dl) return;
  let total = 0;
  for (const c of dl.chunks) total += (c && c.byteLength) || (c && c.length) || 0;
  if (dl.size > 0 && total !== dl.size) {
    toast(I18N.t("desktop.download_incomplete") + ` (${total}/${dl.size})`, "err");
    const log = $("deskXferLog");
    if (log) log.textContent = "↓ ERR size mismatch";
    return;
  }
  if (total === 0) {
    toast(I18N.t("desktop.download_fail"), "err");
    return;
  }
  const blob = new Blob(dl.chunks);
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob); a.download = dl.filename;
  document.body.appendChild(a); a.click();
  setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
  toast(I18N.t("desktop.download_ok") + ": " + dl.filename, "ok");
}

let _deskInputBound = false;
let _deskInputEl = null;
let _deskKeysBound = false;
let _deskPressed = new Set(); // codes currently down — released on blur to avoid stuck remote keys

function deskIsEditableTarget(t) {
  if (!t) return false;
  // Unlock panel + side clipboard/path fields must keep local typing — never
  // forward those keystrokes to the remote session (capture-phase listener).
  if (t.closest && t.closest("#deskUnlockPanel, #deskSide, .desk-unlock-panel, .desk-side")) {
    return true;
  }
  const el = t.nodeType === 3 ? t.parentElement : t; // text node → element
  if (!el || !el.tagName) return false;
  const tag = el.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  if (el.isContentEditable) return true;
  if (el.closest && el.closest("input, textarea, select, [contenteditable='true']")) return true;
  return false;
}

function bindDesktopInput(el) {
  if (!el) return;
  if (_deskInputEl === el && _deskInputBound) return;
  if (_deskInputEl && _deskInputEl !== el) unbindDesktopInput(_deskInputEl);
  _deskInputBound = true;
  _deskInputEl = el;
  if (window.PointerEvent) {
    el.addEventListener("pointermove", onDeskMouseMove);
    el.addEventListener("pointerdown", onDeskMouseDown);
    el.addEventListener("pointerup", onDeskMouseUp);
    el.addEventListener("pointercancel", onDeskMouseUp);
  } else {
    el.addEventListener("mousemove", onDeskMouseMove);
    el.addEventListener("mousedown", onDeskMouseDown);
    el.addEventListener("mouseup", onDeskMouseUp);
  }
  el.addEventListener("contextmenu", onDeskContext);
  el.addEventListener("wheel", onDeskWheel, { passive: false });
  // Keyboard is captured at document level (bindDeskSessionKeys) so typing works
  // even when focus is not on the canvas — commercial RD clients behave this way.
  bindDeskSessionKeys();
}

function unbindDesktopInput(el) {
  el = el || _deskInputEl;
  if (!el || !_deskInputBound) return;
  _deskInputBound = false;
  _deskInputEl = null;
  el.removeEventListener("pointermove", onDeskMouseMove);
  el.removeEventListener("pointerdown", onDeskMouseDown);
  el.removeEventListener("pointerup", onDeskMouseUp);
  el.removeEventListener("pointercancel", onDeskMouseUp);
  el.removeEventListener("mousemove", onDeskMouseMove);
  el.removeEventListener("mousedown", onDeskMouseDown);
  el.removeEventListener("mouseup", onDeskMouseUp);
  el.removeEventListener("contextmenu", onDeskContext);
  el.removeEventListener("wheel", onDeskWheel);
}

function bindDeskSessionKeys() {
  if (_deskKeysBound) return;
  _deskKeysBound = true;
  document.addEventListener("keydown", onDeskGlobalKeyDown, true);
  document.addEventListener("keyup", onDeskGlobalKeyUp, true);
  window.addEventListener("blur", onDeskWindowBlur);
  document.addEventListener("visibilitychange", onDeskVisibility);
  window.addEventListener("pointerup", onDeskWindowPointerUp, true);
}

function unbindDeskSessionKeys() {
  if (!_deskKeysBound) return;
  _deskKeysBound = false;
  deskReleaseAllKeys();
  document.removeEventListener("keydown", onDeskGlobalKeyDown, true);
  document.removeEventListener("keyup", onDeskGlobalKeyUp, true);
  window.removeEventListener("blur", onDeskWindowBlur);
  document.removeEventListener("visibilitychange", onDeskVisibility);
  window.removeEventListener("pointerup", onDeskWindowPointerUp, true);
}

function deskSessionActive() {
  return !!(DESK_WS && DESK_WS.readyState === 1 &&
    DESK_PHASE !== "error" && DESK_PHASE !== "closed" && DESK_PHASE !== "idle");
}

function onDeskGlobalKeyDown(ev) {
  const stage = $("deskStage");
  if (ev.key === "Escape" && stage && stage.classList.contains("is-fullscreen-fallback")) {
    ev.preventDefault();
    ev.stopPropagation();
    stage.classList.remove("is-fullscreen-fallback");
    return;
  }
  if (!deskSessionActive() || DESK_META.viewOnly) return;
  if (deskIsEditableTarget(ev.target)) return;
  // Ignore browser chord that closes the tab / reloads — still forward most keys.
  if (ev.key === "F5" || (ev.metaKey || ev.ctrlKey) && (ev.key === "r" || ev.key === "R" || ev.key === "w" || ev.key === "W")) {
    // Allow native browser shortcuts; do not forward.
    return;
  }
  ev.preventDefault();
  ev.stopPropagation();
  if (ev.repeat && _deskPressed.has(ev.code)) {
    // Still forward repeats — remote apps expect key repeat for arrows/backspace.
  }
  _deskPressed.add(ev.code);
  // Include modifier flags so the agent can choose UNICODE vs VK (shortcuts).
  deskSendJSON("B", {
    down: true, key: ev.key, code: ev.code, vk: 0,
    shift: !!ev.shiftKey, ctrl: !!ev.ctrlKey, alt: !!ev.altKey, meta: !!ev.metaKey
  });
}

function onDeskGlobalKeyUp(ev) {
  if (!deskSessionActive() || DESK_META.viewOnly) return;
  if (deskIsEditableTarget(ev.target)) return;
  ev.preventDefault();
  ev.stopPropagation();
  _deskPressed.delete(ev.code);
  deskSendJSON("B", {
    down: false, key: ev.key, code: ev.code, vk: 0,
    shift: !!ev.shiftKey, ctrl: !!ev.ctrlKey, alt: !!ev.altKey, meta: !!ev.metaKey
  });
}

function deskReleaseAllKeys() {
  if (!_deskPressed.size || !DESK_WS || DESK_WS.readyState !== 1) {
    _deskPressed.clear();
    return;
  }
  for (const code of Array.from(_deskPressed)) {
    deskSendJSON("B", { down: false, key: "", code, vk: 0 });
  }
  _deskPressed.clear();
}

function onDeskWindowBlur() { deskReleaseAllKeys(); }
function onDeskVisibility() {
  if (document.hidden) deskReleaseAllKeys();
}
function onDeskWindowPointerUp(ev) {
  if (!deskSessionActive() || !_deskInputEl || !DESK_LAST_PTR) return;
  if (ev.target === _deskInputEl || (_deskInputEl.contains && _deskInputEl.contains(ev.target))) return;
  // Release buttons if the pointer left the stream surface mid-drag.
  const btn = ev.button === 2 ? 2 : ev.button === 1 ? 3 : 1;
  deskSendJSON("M", { x: DESK_LAST_PTR.x, y: DESK_LAST_PTR.y, action: "up", btn, norm: true });
}

// Map pointer → remote desktop [0,1] fractions (object-fit:contain letterbox).
// Prefer the stage rect + DESK_META aspect so JPEG scale changes never skew hits.
// Clamps instead of returning null so edge / subpixel misses still inject.
function deskNormXY(ev, el) {
  const stage = $("deskStage");
  const rectEl = stage || el;
  if (!rectEl) return null;
  const rect = rectEl.getBoundingClientRect();
  if (rect.width < 2 || rect.height < 2) return null;
  const bw = DESK_META.w || (el && (el.videoWidth || el.width)) || rect.width;
  const bh = DESK_META.h || (el && (el.videoHeight || el.height)) || rect.height;
  const scale = Math.min(rect.width / Math.max(1, bw), rect.height / Math.max(1, bh));
  const dispW = Math.max(1, bw * scale);
  const dispH = Math.max(1, bh * scale);
  const offX = (rect.width - dispW) / 2;
  const offY = (rect.height - dispH) / 2;
  let nx = (ev.clientX - rect.left - offX) / dispW;
  let ny = (ev.clientY - rect.top - offY) / dispH;
  if (nx < -0.02 || nx > 1.02 || ny < -0.02 || ny > 1.02) return null;
  nx = Math.min(1, Math.max(0, nx));
  ny = Math.min(1, Math.max(0, ny));
  return { x: nx, y: ny };
}
function deskSendJSON(typ, obj) {
  if (!DESK_WS || DESK_WS.readyState !== 1) return;
  if (DESK_META.viewOnly && (typ === "M" || typ === "W" || typ === "B" || typ === "A")) return;
  const payload = new TextEncoder().encode(JSON.stringify(obj));
  const buf = new Uint8Array(1 + payload.length);
  buf[0] = typ.charCodeAt(0); buf.set(payload, 1); DESK_WS.send(buf);
}
let _deskLastMove = 0;
function onDeskMouseMove(ev) {
  if (!deskSessionActive() || DESK_META.viewOnly) return;
  const now = Date.now(); if (now - _deskLastMove < 16) return; _deskLastMove = now;
  const p = deskNormXY(ev, deskActiveSurface() || ev.currentTarget);
  if (!p) return;
  DESK_LAST_PTR = p;
  deskSendJSON("M", { x: p.x, y: p.y, action: "move", btn: 0, norm: true });
}
function onDeskMouseDown(ev) {
  if (!deskSessionActive() || DESK_META.viewOnly) return;
  ev.preventDefault();
  const surf = deskActiveSurface() || ev.currentTarget;
  try { if (surf && surf.focus) surf.focus({ preventScroll: true }); } catch (e) {}
  // Do NOT setPointerCapture on the stage — a stuck capture steals clicks from
  // the quality/codec selects and makes the toolbar look "dead".
  const p = deskNormXY(ev, surf);
  if (!p) return;
  DESK_LAST_PTR = p;
  const btn = ev.button === 2 ? 2 : ev.button === 1 ? 3 : 1;
  deskSendJSON("M", { x: p.x, y: p.y, action: "down", btn, norm: true });
}
function onDeskMouseUp(ev) {
  if (!deskSessionActive() || DESK_META.viewOnly) return;
  ev.preventDefault();
  const surf = deskActiveSurface() || ev.currentTarget;
  const p = deskNormXY(ev, surf) || DESK_LAST_PTR;
  if (!p) return;
  const btn = ev.button === 2 ? 2 : ev.button === 1 ? 3 : 1;
  deskSendJSON("M", { x: p.x, y: p.y, action: "up", btn, norm: true });
}
function onDeskContext(ev) { ev.preventDefault(); }
function onDeskWheel(ev) {
  ev.preventDefault();
  if (!deskSessionActive() || DESK_META.viewOnly) return;
  deskSendJSON("W", { delta: ev.deltaY > 0 ? -1 : 1 });
}

function applyDeskInputMeta(meta) {
  if (!meta || typeof meta !== "object") return;
  if (meta.os) DESK_META.os = meta.os;
  if (meta.desktop != null) DESK_META.desktop = meta.desktop || "";
  if (meta.secure_desktop != null) DESK_META.secureDesktop = !!meta.secure_desktop;
  else if (meta.desktop != null) DESK_META.secureDesktop = deskOnSecureDesktop();
  if (meta.input_desktop_ok != null) DESK_META.inputDesktopOk = !!meta.input_desktop_ok;
  if (meta.lock_hint) DESK_META.lockHint = meta.lock_hint;
  if (meta.features && typeof meta.features === "object") {
    DESK_META.features = { ...DESK_META.features, ...meta.features };
  }
  const hint = $("deskLockHint");
  if (hint && DESK_META.lockHint) {
    hint.hidden = false;
    hint.textContent = DESK_META.lockHint;
    hint.classList.toggle("secure", deskOnSecureDesktop());
  }
  const cadBtn = $("deskCadBtn");
  if (cadBtn) {
    // On Winlogon always enable CAD — older agents may omit features.cad.
    const cadOK = !DESK_META.viewOnly && (
      (DESK_META.features && DESK_META.features.cad) ||
      deskOnSecureDesktop() ||
      String(DESK_META.os || "").toLowerCase() === "windows"
    );
    cadBtn.disabled = !cadOK;
    cadBtn.title = cadOK
      ? I18N.t("desktop.cad", "Ctrl+Alt+Del")
      : I18N.t("desktop.cad_unsupported", "当前 Agent/平台不支持 SendSAS（Windows 服务模式可用）");
  }
  document.querySelectorAll("#deskLockTools [data-desk-act]").forEach((btn) => {
    if (btn.id === "deskCadBtn") return;
    btn.disabled = !!DESK_META.viewOnly;
  });
}

function refreshDeskInputStatus() {
  if (DESK_PHASE === "error" || DESK_PHASE === "closed") return;
  if (DESK_META.viewOnly) {
    setDesktopStatus(I18N.t("desktop.view_only"), false);
    return;
  }
  if (DESK_META.inputDesktopOk === false) {
    setDesktopStatus(I18N.t("desktop.input_detached", "已连接 · 仅画面（输入桌面未附着）"), false);
    return;
  }
  let msg = I18N.t("desktop.connected");
  if (DESK_META.desktop) {
    msg += " · " + DESK_META.desktop;
  }
  if (DESK_META.os) {
    msg += " · " + DESK_META.os;
  }
  setDesktopStatus(msg, false);
}

function deskSendAction(obj) {
  if (!DESK_WS || DESK_WS.readyState !== 1) {
    toast(I18N.t("desktop.not_connected"), "err");
    return false;
  }
  if (DESK_META.viewOnly) {
    toast(I18N.t("desktop.view_only"), "err");
    return false;
  }
  const body = { ...obj, screen_w: DESK_META.w || 0, screen_h: DESK_META.h || 0 };
  // Immediate feedback — do not wait for agent ack (CAD used to look like a no-op).
  const act = String((obj && obj.action) || "").toLowerCase();
  if (act === "cad") toast(I18N.t("desktop.cad_sending", "正在发送 Ctrl+Alt+Del…"), "ok");
  else if (act === "wake") toast(I18N.t("desktop.wake_sending", "正在唤醒…"), "ok");
  else if (act === "chord") toast(I18N.t("desktop.chord_sending", "正在发送快捷键…"), "ok");
  else if (act === "unlock") toast(I18N.t("desktop.unlock_sending", "正在发送解锁凭据…"), "ok");
  else if (act === "paste") toast(I18N.t("desktop.paste_sending", "正在粘贴到远程…"), "ok");
  deskSendJSON("A", body);
  return true;
}

function openDeskUnlockPanel(show) {
  const p = $("deskUnlockPanel");
  if (!p) return;
  p.hidden = !show;
  if (show) {
    const u = $("deskUnlockUser");
    const pw = $("deskUnlockPass");
    if (u) u.value = "";
    if (pw) pw.value = "";
    if (pw) pw.focus();
  }
}

async function deskSendUnlockCredentials() {
  const userEl = $("deskUnlockUser");
  const passEl = $("deskUnlockPass");
  const user = (userEl && userEl.value) || "";
  const pass = (passEl && passEl.value) || "";
  if (!pass && !user) {
    toast(I18N.t("desktop.unlock_empty", "请输入密码（或用户名）"), "err");
    return;
  }
  const ok = typeof uiConfirm === "function"
    ? await uiConfirm({
        title: I18N.t("desktop.unlock", "解锁"),
        message: I18N.t("desktop.unlock_confirm", "确认向远程主机发送解锁凭据？内容不会写入日志。"),
        tone: "danger"
      })
    : confirm(I18N.t("desktop.unlock_confirm", "确认向远程主机发送解锁凭据？内容不会写入日志。"));
  if (!ok) return;
  // One agent-side unlock sequence (wake + type) — avoids multi-RTT pacing that
  // made lock-screen passwords appear one character at a time.
  if (DESK_META.features && DESK_META.features.unlock) {
    deskSendAction({
      action: "unlock",
      user,
      text: pass,
      enter: true,
      screen_w: DESK_META.w || 0,
      screen_h: DESK_META.h || 0
    });
  } else {
    // Older agents without unlock action: short paced type_text fallback.
    deskSendAction({ action: "wake" });
    await new Promise((r) => setTimeout(r, 80));
    if (user) {
      deskSendAction({ action: "type_text", text: user, enter: false });
      await new Promise((r) => setTimeout(r, 40));
      deskSendAction({ action: "chord", chord: "tab" });
      await new Promise((r) => setTimeout(r, 30));
    }
    deskSendAction({ action: "type_text", text: pass, enter: true });
  }
  if (passEl) passEl.value = "";
  if (userEl) userEl.value = "";
  openDeskUnlockPanel(false);
}

function onDesktopUIClick(e) {
  const t = e.target;
  const actBtn = t.closest("[data-desk-act]");
  if (actBtn) {
    e.preventDefault();
    e.stopPropagation();
    if (actBtn.disabled) {
      toast(actBtn.title || I18N.t("desktop.cad_unsupported", "当前不支持该操作"), "err");
      return;
    }
    const act = actBtn.getAttribute("data-desk-act");
    if (act === "cad") deskSendAction({ action: "cad" });
    else if (act === "wake") deskSendAction({ action: "wake" });
    else if (act === "unlock") openDeskUnlockPanel(true);
    else if (act === "chord") deskSendAction({ action: "chord", chord: actBtn.getAttribute("data-desk-chord") || "esc" });
    return;
  }
  if (t.id === "deskUnlockSend" || t.closest("#deskUnlockSend")) {
    deskSendUnlockCredentials();
    return;
  }
  if (t.id === "deskUnlockCancel" || t.closest("#deskUnlockCancel")) {
    openDeskUnlockPanel(false);
    return;
  }
  if (t.id === "deskSideToggle" || t.closest("#deskSideToggle")) {
    const side = $("deskSide");
    if (side) {
      const collapsed = side.classList.toggle("is-collapsed");
      const btn = $("deskSideToggle");
      if (btn) btn.setAttribute("aria-expanded", collapsed ? "false" : "true");
    }
    return;
  }
  if (t.id === "deskDisconnect" || t.closest("#deskDisconnect")) {
    DESK_INTENTIONAL_CLOSE = true;
    closeDesktopMask();
    return;
  }
  if (t.id === "deskFullscreen" || t.closest("#deskFullscreen")) {
    toggleDeskFullscreen();
    return;
  }
  if (t.id === "deskUploadBtn" || t.closest("#deskUploadBtn")) { const inp = $("deskFileInput"); if (inp) inp.click(); return; }
  if (t.id === "deskDownloadBtn" || t.closest("#deskDownloadBtn")) { deskStartDownload(); return; }
  if (t.id === "deskClipApply" || t.closest("#deskClipApply") || t.id === "deskClipSend" || t.closest("#deskClipSend")) {
    deskPushClipboard(); return;
  }
  if (t.id === "deskSessions" || t.closest("#deskSessions")) { openDeskSessions(); return; }
  if (t.id === "deskReplayClose" || t.closest("#deskReplayClose")) {
    const p = $("deskReplayPane"); if (p) p.hidden = true; return;
  }
  const play = t.closest("[data-desk-replay]");
  if (play) { playDeskReplay(play.getAttribute("data-desk-replay")); }
}

function deskFullscreenElement() {
  return document.fullscreenElement || document.webkitFullscreenElement || document.msFullscreenElement || null;
}

function requestDeskFullscreen(el) {
  if (!el) return Promise.reject(new Error("no element"));
  const req = el.requestFullscreen || el.webkitRequestFullscreen || el.msRequestFullscreen;
  if (!req) return Promise.reject(new Error("fullscreen unsupported"));
  return Promise.resolve(req.call(el));
}

function exitDeskFullscreen() {
  const exit = document.exitFullscreen || document.webkitExitFullscreen || document.msExitFullscreen;
  if (!exit) return Promise.resolve();
  return Promise.resolve(exit.call(document)).catch(() => {});
}

function toggleDeskFullscreen() {
  const stage = $("deskStage");
  const active = deskFullscreenElement();
  if (active) {
    exitDeskFullscreen();
    return;
  }
  if (!stage) return;
  if (stage.classList.contains("is-fullscreen-fallback")) {
    stage.classList.remove("is-fullscreen-fallback");
    return;
  }
  // Fullscreen only the stream stage: title, toolbar and file-transfer sidebar
  // must disappear so the remote desktop occupies the entire display.
  requestDeskFullscreen(stage).catch(() => {
    stage.classList.add("is-fullscreen-fallback");
  });
}

function onDesktopUIChange(e) {
  if (e.target && e.target.id === "deskQuality") {
    const p = qualityPreset(e.target.value);
    DESK_QUALITY = { ...DESK_QUALITY, ...p };
    sendDeskQuality();
    if (!(DESK_WS && DESK_WS.readyState === 1)) {
      toast(I18N.t("desktop.not_connected"), "err");
      return;
    }
    // Prefer agent quality_ack; if Agent is old / Q dropped, still give feedback.
    const label = (e.target.selectedOptions[0] && e.target.selectedOptions[0].text) || e.target.value;
    clearTimeout(window._deskQToastTimer);
    window._deskQToastTimer = setTimeout(() => {
      toast(I18N.t("desktop.quality_applied") + ": " + label
        + ` (q${p.quality} · ${p.fps}fps)`, "ok");
    }, 1200);
    return;
  }
  if (e.target && e.target.id === "deskCodec") {
    DESK_QUALITY.codec = e.target.value === "h265" ? "h265" : (e.target.value === "h264" ? "h264" : "jpeg");
    if (DESK_QUALITY.codec === "jpeg") closeDeskMSE();
    sendDeskQuality();
    if (DESK_WS && DESK_WS.readyState === 1) {
      toast(I18N.t("desktop.codec") + ": " + DESK_QUALITY.codec.toUpperCase(), "ok");
    }
    return;
  }
  if (e.target && e.target.id === "deskResolution") {
    const v = String(e.target.value || "");
    if (!v) return; // "保持远端不变"
    const client = readDeskClientSize();
    if (v === "fit") {
      deskSendJSON("A", { action: "set_resolution", client_w: client.client_w, client_h: client.client_h });
    } else {
      const [w, h] = v.split("x").map(n => parseInt(n, 10) || 0);
      deskSendJSON("A", { action: "set_resolution", w, h });
    }
    return;
  }
  if (e.target && e.target.id === "deskMonitor") {
    DESK_QUALITY.monitor = parseInt(e.target.value, 10) || 0;
    deskSendJSON("N", { id: DESK_QUALITY.monitor });
    sendDeskQuality(); return;
  }
  if (e.target && e.target.id === "deskClipAutoSync") {
    DESK_CLIP_AUTOSYNC = !!e.target.checked;
    return;
  }
  if (e.target && e.target.id === "deskFileInput" && e.target.files && e.target.files[0]) {
    deskStartUpload(e.target.files[0]); e.target.value = "";
  }
}

function onDeskDrop(e) {
  e.preventDefault();
  const stage = $("deskStage"); if (stage) stage.classList.remove("drag");
  const f = e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files[0];
  if (f) deskStartUpload(f);
}

function deskPushClipboard() {
  const box = $("deskClipBox");
  const send = (text) => {
    if (!text) return;
    if (box) box.value = text;
    // Prefer paste action: set remote clipboard + Ctrl+V into focused control.
    if (DESK_META.features && DESK_META.features.paste) {
      deskSendAction({ action: "paste", text });
    } else {
      deskSendJSON("C", { text, paste: true });
    }
    toast(I18N.t("desktop.clip_sent"), "ok");
  };
  if (box && box.value) { send(box.value); return; }
  if (navigator.clipboard && navigator.clipboard.readText) {
    navigator.clipboard.readText().then(send).catch(() => toast(I18N.t("desktop.clip_fail"), "err"));
  }
}

async function deskStartUpload(file) {
  if (!DESK_WS || DESK_WS.readyState !== 1) { toast(I18N.t("desktop.not_connected"), "err"); return; }
  if (file.size > 100 * 1024 * 1024) { toast(I18N.t("term.file_too_large"), "err"); return; }
  let path = (($("deskUploadPath") && $("deskUploadPath").value) || "").trim();
  if (!path) path = file.name;
  else if (path.endsWith("/") || path.endsWith("\\")) path = path + file.name;
  const meta = new TextEncoder().encode(JSON.stringify({ filename: file.name, size: file.size, target_path: path }));
  const fbuf = new Uint8Array(1 + meta.length); fbuf[0] = "f".charCodeAt(0); fbuf.set(meta, 1); DESK_WS.send(fbuf);
  const chunkSize = 48 * 1024; let offset = 0;
  const log = $("deskXferLog");
  toast(I18N.t("desktop.uploading") + ": " + file.name, "info");
  while (offset < file.size) {
    if (DESK_WS.readyState !== 1) { toast(I18N.t("desktop.upload_fail"), "err"); return; }
    // Backpressure: wait until the browser WS buffer drains so we don't stall the
    // input/control relay behind a multi-MB upload.
    while (DESK_WS.bufferedAmount > 512 * 1024) {
      await new Promise(r => setTimeout(r, 20));
      if (DESK_WS.readyState !== 1) { toast(I18N.t("desktop.upload_fail"), "err"); return; }
    }
    const slice = file.slice(offset, offset + chunkSize);
    const ab = await slice.arrayBuffer();
    const u = new Uint8Array(ab);
    const buf = new Uint8Array(1 + u.length); buf[0] = "u".charCodeAt(0); buf.set(u, 1); DESK_WS.send(buf);
    offset += u.length;
    if (log) log.textContent = `↑ ${Math.min(100, Math.round(offset / file.size * 100))}% ${file.name}`;
  }
  DESK_WS.send(new Uint8Array(["e".charCodeAt(0)]));
}

function deskStartDownload() {
  if (!DESK_WS || DESK_WS.readyState !== 1) { toast(I18N.t("desktop.not_connected"), "err"); return; }
  const path = (($("deskDownloadPath") && $("deskDownloadPath").value) || "").trim();
  if (!path) { toast(I18N.t("desktop.download_ph"), "err"); return; }
  const meta = new TextEncoder().encode(JSON.stringify({ remote_path: path }));
  const buf = new Uint8Array(1 + meta.length); buf[0] = "d".charCodeAt(0); buf.set(meta, 1); DESK_WS.send(buf);
}

async function openDeskSessions() {
  const pane = $("deskReplayPane");
  const list = $("deskSessionsList");
  if (!pane || !list) return;
  pane.hidden = false;
  list.innerHTML = I18N.t("ui.loading");
  try {
    const sessions = await fetch(`${API}/desktop/sessions`, { credentials: "include" }).then(r => r.json());
    if (!Array.isArray(sessions) || !sessions.length) {
      list.innerHTML = `<div class="empty-line">${esc(I18N.t("desktop.no_sessions"))}</div>`;
      return;
    }
    list.innerHTML = sessions.map(s => `
      <div class="desk-sess-row">
        <div><b>${esc(s.hostname || s.host_id)}</b> · ${esc(s.operator || "")} · ${s.frames || 0} ${esc(I18N.t("desktop.frames"))}
          ${s.active ? `<span class="tag">${esc(I18N.t("desktop.live"))}</span>` : ""}</div>
        <button type="button" class="btn sm" data-desk-replay="${esc(s.id)}">${esc(I18N.t("desktop.replay"))}</button>
      </div>`).join("");
  } catch (e) { list.innerHTML = `<div class="empty-line err">${esc(String(e))}</div>`; }
}

async function playDeskReplay(id) {
  const canvas = $("deskReplayCanvas");
  if (!canvas) return;
  canvas.style.display = "block";
  const ctx = canvas.getContext("2d");
  try {
    const data = await fetch(`${API}/desktop/sessions/${encodeURIComponent(id)}/replay`, { credentials: "include" }).then(r => r.json());
    const frames = data.frames || [];
    let i = 0;
    const b64 = (str) => {
      const bin = atob(str);
      const u8 = new Uint8Array(bin.length);
      for (let j = 0; j < bin.length; j++) u8[j] = bin.charCodeAt(j);
      return u8;
    };
    const tick = () => {
      if (i >= frames.length) return;
      const f = frames[i++];
      // 差分帧：画在上一张关键帧之上（不能改画布尺寸，那会把底图清掉）。
      if (f.type === "tiles" && f.data) {
        drawDeskTiles(canvas, b64(f.data), null);
        setTimeout(tick, 60);
        return;
      }
      if (f.type === "jpeg" && f.data) {
        const bin = atob(f.data);
        const u8 = new Uint8Array(bin.length);
        for (let j = 0; j < bin.length; j++) u8[j] = bin.charCodeAt(j);
        const blob = new Blob([u8], { type: "image/jpeg" });
        const url = URL.createObjectURL(blob);
        const img = new Image();
        img.onload = () => {
          canvas.width = img.width; canvas.height = img.height;
          ctx.drawImage(img, 0, 0); URL.revokeObjectURL(url);
          setTimeout(tick, 120);
        };
        img.src = url;
      } else setTimeout(tick, 40);
    };
    tick();
  } catch (e) { toast(String(e), "err"); }
}

function closeDesktopWS() {
  unbindDeskSessionKeys();
  if (DESK_WS) { try { DESK_WS.close(); } catch (e) {} DESK_WS = null; }
  unbindDesktopInput($("deskStage"));
  unbindDesktopInput($("deskCanvas"));
  unbindDesktopInput($("deskVideo"));
  closeDeskMSE();
}

function closeDesktopMask() {
  DESK_INTENTIONAL_CLOSE = true;
  closeDesktopWS();
  DESK_PHASE = "idle";
  DESK_GOT_FRAME = false;
  DESK_RETRY = 0;
  exitDeskFullscreen().catch(() => {});
  const modal = document.querySelector("#desktopMask .desk-modal");
  if (modal) modal.classList.remove("is-max");
  const mask = $("desktopMask");
  if (mask) mask.classList.remove("show");
}
