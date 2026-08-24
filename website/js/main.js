/* ============================================================
   AIOps · 营销网站交互逻辑 v2
   增强：视差滚动 · 鼠标光效 · 性能优化
   ============================================================ */
"use strict";
(function(){

/* ===== 导航栏滚动效果 ===== */
var navbar = document.querySelector(".navbar");
if (navbar) {
  var navTicking = false;
  window.addEventListener("scroll", function() {
    if (!navTicking) {
      navTicking = true;
      requestAnimationFrame(function() {
        navbar.classList.toggle("scrolled", window.scrollY > 20);
        navTicking = false;
      });
    }
  });
}

/* ===== 移动端菜单 ===== */
var toggle = document.querySelector(".nav-toggle");
var links = document.querySelector(".nav-links");
if (toggle && links) {
  toggle.setAttribute("aria-label", "菜单");
  toggle.setAttribute("aria-expanded", "false");
  toggle.addEventListener("click", function(e) {
    e.stopPropagation();
    var open = links.classList.toggle("open");
    toggle.setAttribute("aria-expanded", open ? "true" : "false");
  });
  links.querySelectorAll("a").forEach(function(a) {
    a.addEventListener("click", function() {
      links.classList.remove("open");
      toggle.setAttribute("aria-expanded", "false");
    });
  });
  document.addEventListener("click", function(e) {
    if (links.classList.contains("open") && !links.contains(e.target) && !toggle.contains(e.target)) {
      links.classList.remove("open");
      toggle.setAttribute("aria-expanded", "false");
    }
  });
  document.addEventListener("keydown", function(e) {
    if (e.key === "Escape" && links.classList.contains("open")) {
      links.classList.remove("open");
      toggle.setAttribute("aria-expanded", "false");
    }
  });
  window.addEventListener("resize", function() {
    if (window.innerWidth > 900 && links.classList.contains("open")) {
      links.classList.remove("open");
      toggle.setAttribute("aria-expanded", "false");
    }
  });
}

/* ===== 导航下拉分组（产品） ===== */
(function(){
  var drops = document.querySelectorAll(".nav-dropdown");
  drops.forEach(function(d){
    var btn = d.querySelector(".nav-dropdown-toggle");
    if (!btn) return;
    btn.addEventListener("click", function(e){
      e.stopPropagation();
      var open = d.classList.toggle("open");
      btn.setAttribute("aria-expanded", open ? "true" : "false");
    });
    d.querySelectorAll(".nav-dropdown-menu a").forEach(function(a){
      a.addEventListener("click", function(){
        d.classList.remove("open");
        btn.setAttribute("aria-expanded", "false");
      });
    });
  });
  document.addEventListener("click", function(e){
    drops.forEach(function(d){
      if (d.classList.contains("open") && !d.contains(e.target)){
        d.classList.remove("open");
        var b = d.querySelector(".nav-dropdown-toggle"); if (b) b.setAttribute("aria-expanded", "false");
      }
    });
  });
  document.addEventListener("keydown", function(e){
    if (e.key === "Escape") {
      drops.forEach(function(d){ d.classList.remove("open"); var b = d.querySelector(".nav-dropdown-toggle"); if (b) b.setAttribute("aria-expanded", "false"); });
    }
  });
  window.addEventListener("resize", function(){
    if (window.innerWidth > 900) drops.forEach(function(d){ d.classList.remove("open"); var b = d.querySelector(".nav-dropdown-toggle"); if (b) b.setAttribute("aria-expanded", "false"); });
  });
})();

/* ===== Hero 视差光效（桌面端 · 尊重 reduced-motion） ===== */
(function(){
  var hero = document.querySelector(".hero");
  if (!hero) return;
  var bg = hero.querySelector(".hero-bg");
  if (!bg) return;
  var reduce = false;
  try { reduce = window.matchMedia("(prefers-reduced-motion: reduce)").matches; } catch (e) {}
  if (reduce) return;

  var hoverX = 0, hoverY = 0, curX = 0, curY = 0;
  var isDesktop = window.innerWidth > 768;
  var rafId = 0;
  var scrollY = 0;

  function paint() {
    rafId = 0;
    if (!isDesktop) return;
    curX += (hoverX - curX) * 0.06;
    curY += (hoverY - curY) * 0.06;
    bg.style.transform = "translate(" + (curX * 12).toFixed(2) + "px, " + ((curY * 12) + scrollY * 0.12).toFixed(2) + "px)";
  }
  function schedule() {
    if (!rafId) rafId = requestAnimationFrame(paint);
  }

  document.addEventListener("mousemove", function(e) {
    if (!isDesktop) return;
    hoverX = (e.clientX / window.innerWidth - 0.5) * 2;
    hoverY = (e.clientY / window.innerHeight - 0.5) * 2;
    schedule();
  });
  window.addEventListener("scroll", function() {
    scrollY = window.scrollY || 0;
    if (isDesktop) schedule();
  }, { passive: true });
  window.addEventListener("resize", function() {
    isDesktop = window.innerWidth > 768;
    if (!isDesktop) bg.style.transform = "";
  });
})();

/* ===== 主题切换 ===== */
(function(){
  var STORE_KEY = "aiops_theme";
  var root = document.documentElement;

  var SUN = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4.5"/><line x1="12" y1="2" x2="12" y2="5"/><line x1="12" y1="19" x2="12" y2="22"/><line x1="2" y1="12" x2="5" y2="12"/><line x1="19" y1="12" x2="22" y2="12"/><line x1="4.5" y1="4.5" x2="6.6" y2="6.6"/><line x1="17.4" y1="17.4" x2="19.5" y2="19.5"/><line x1="4.5" y1="19.5" x2="6.6" y2="17.4"/><line x1="17.4" y1="6.6" x2="19.5" y2="4.5"/></svg>';
  var MOON = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>';

  function currentTheme(){ return root.getAttribute("data-theme") === "light" ? "light" : "dark"; }

  function applyThemeMeta(theme){
    var meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute("content", theme === "light" ? "#f5f7fc" : "#070b14");
  }

  function paintToggle(btn, theme){
    if (!btn) return;
    if (theme === "light"){ btn.innerHTML = MOON; btn.setAttribute("aria-label", "切换到深色模式"); btn.setAttribute("title", "切换到深色模式"); }
    else { btn.innerHTML = SUN; btn.setAttribute("aria-label", "切换到浅色模式"); btn.setAttribute("title", "切换到浅色模式"); }
  }

  function setTheme(theme, persist){
    root.setAttribute("data-theme", theme);
    root.style.colorScheme = theme;
    applyThemeMeta(theme);
    paintToggle(document.getElementById("themeToggle"), theme);
    if (persist){
      try { localStorage.setItem(STORE_KEY, theme); } catch(e){}
      try {
        var url = new URL(window.location.href);
        url.searchParams.set("theme", theme);
        window.history.replaceState({}, "", url.toString());
      } catch(e){}
      syncLinkThemes();
    }
    try { document.dispatchEvent(new CustomEvent("theme:changed", { detail: { theme: theme } })); } catch(e){}
  }

  function currentLang(){
    try { if (window.AIOpsI18n && window.AIOpsI18n.getLang) return window.AIOpsI18n.getLang(); } catch(e){}
    try { var p = new URLSearchParams(location.search); var l = p.get("lang"); if (l) return l; } catch(e){}
    try { return localStorage.getItem("aiops_lang") || "zh-CN"; } catch(e){ return "zh-CN"; }
  }

  function syncLinkThemes(){
    var theme = currentTheme();
    var lang = currentLang();
    document.querySelectorAll("a[href]").forEach(function(a){
      var href = a.getAttribute("href");
      if (!href || href.charAt(0) === "#" || a.hasAttribute("download")) return;
      var url;
      try { url = new URL(href, location.href); } catch(e){ return; }
      if (url.origin !== location.origin) return;
      if (!/\.html/i.test(url.pathname)) return;
      url.searchParams.set("theme", theme);
      if (lang) url.searchParams.set("lang", lang);
      a.setAttribute("href", url.pathname + url.search + url.hash);
    });
  }

  function injectToggle(){
    var nav = document.querySelector(".nav-inner");
    if (!nav || document.getElementById("themeToggle")) return;
    var btn = document.createElement("button");
    btn.id = "themeToggle";
    btn.type = "button";
    btn.className = "theme-toggle";
    var cta = nav.querySelector(".nav-cta");
    if (cta) nav.insertBefore(btn, cta); else nav.appendChild(btn);
    btn.addEventListener("click", function(){
      setTheme(currentTheme() === "light" ? "dark" : "light", true);
    });
    paintToggle(btn, currentTheme());
  }

  injectToggle();
  applyThemeMeta(currentTheme());
  syncLinkThemes();
  try { document.addEventListener("lang:changed", function(){ syncLinkThemes(); }); } catch(e){}

  try {
    var mq = window.matchMedia("(prefers-color-scheme: light)");
    var onChange = function(e){
      var stored; try { stored = localStorage.getItem(STORE_KEY); } catch(err){}
      if (stored === "light" || stored === "dark") return;
      setTheme(e.matches ? "light" : "dark", false);
    };
    if (mq.addEventListener) mq.addEventListener("change", onChange);
    else if (mq.addListener) mq.addListener(onChange);
  } catch(e){}
})();

/* ===== 移动端吸底 CTA ===== */
(function(){
  var bar = document.createElement("a");
  bar.className = "mobile-cta";
  bar.href = "https://github.com/sreyun/aiops-monitor";
  bar.target = "_blank";
  bar.rel = "noopener";
  var fallbackCta = "开始使用";
  try {
    if (window.AIOpsI18n && window.AIOpsI18n.t) fallbackCta = window.AIOpsI18n.t("nav.ctaNew") || fallbackCta;
  } catch (e) {}
  bar.innerHTML = '<span data-i18n="nav.ctaNew">' + fallbackCta + '</span>' +
    '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14M13 6l6 6-6 6"/></svg>';
  document.body.appendChild(bar);
  /* i18n 已先执行时补翻译；语言切换时同步 */
  function applyCtaI18n() {
    var el = bar.querySelector("[data-i18n]");
    if (!el) return;
    try {
      if (window.AIOpsI18n && window.AIOpsI18n.t) {
        var v = window.AIOpsI18n.t(el.getAttribute("data-i18n"));
        if (v && v !== el.getAttribute("data-i18n")) el.textContent = v;
      }
    } catch (e) {}
  }
  applyCtaI18n();
  try { document.addEventListener("lang:changed", applyCtaI18n); } catch (e) {}

  var ticking = false;
  function update(){
    ticking = false;
    var mobile = window.innerWidth <= 640;
    var show = mobile && window.scrollY > 600;
    if (show) {
      bar.classList.add("show");
      document.body.classList.add("has-mobile-cta");
    } else {
      bar.classList.remove("show");
      document.body.classList.remove("has-mobile-cta");
    }
  }
  window.addEventListener("scroll", function(){ if (!ticking){ ticking = true; requestAnimationFrame(update); } }, { passive: true });
  window.addEventListener("resize", update);
  update();
})();

/* SVG a11y */
document.querySelectorAll("svg").forEach(function(s) {
  if (!s.getAttribute("role") && !s.getAttribute("aria-hidden")) {
    s.setAttribute("aria-hidden", "true");
  }
});

/* ===== 滚动渐入动画 ===== */
var observer = new IntersectionObserver(function(entries) {
  entries.forEach(function(e) {
    if (e.isIntersecting) {
      e.target.classList.add("visible");
      observer.unobserve(e.target);
    }
  });
}, { threshold: 0.08, rootMargin: "0px 0px -40px 0px" });

function observeReveals() {
  document.querySelectorAll(".reveal:not(.visible)").forEach(function(el) {
    observer.observe(el);
  });
}
observeReveals();
window.addEventListener("reveal:refresh", observeReveals);

/* 错开动画延迟 */
document.querySelectorAll(".pain-card, .feature-card").forEach(function(el, i) {
  el.style.transitionDelay = (i % 4) * 70 + "ms";
});

/* ===== 数字滚动动画 ===== */
function animateNumber(el, target, suffix) {
  var start = 0;
  var duration = 1600;
  var startTime = null;
  suffix = suffix || "";
  function step(ts) {
    if (!startTime) startTime = ts;
    var progress = Math.min((ts - startTime) / duration, 1);
    var eased = 1 - Math.pow(1 - progress, 4);
    el.textContent = Math.floor(eased * target) + suffix;
    if (progress < 1) requestAnimationFrame(step);
  }
  requestAnimationFrame(step);
}

var numObserver = new IntersectionObserver(function(entries) {
  entries.forEach(function(e) {
    if (e.isIntersecting && e.target.dataset.count) {
      animateNumber(e.target, parseInt(e.target.dataset.count), e.target.dataset.suffix || "");
      numObserver.unobserve(e.target);
    }
  });
}, { threshold: 0.5 });

document.querySelectorAll("[data-count]").forEach(function(el) {
  numObserver.observe(el);
});

/* ===== 平滑滚动到锚点 ===== */
document.querySelectorAll('a[href^="#"]').forEach(function(a) {
  a.addEventListener("click", function(e) {
    var href = this.getAttribute("href");
    if (href.length > 1) {
      var target = document.querySelector(href);
      if (target) {
        e.preventDefault();
        target.scrollIntoView({ behavior: "smooth", block: "start" });
      }
    }
  });
});

/* ===== 功能页子导航联动（scrollspy）===== */
(function(){
  var rail = document.getElementById("featSubnav");
  if (!rail) return;
  var links = {};
  rail.querySelectorAll(".feat-subnav-link").forEach(function(l){
    links[l.getAttribute("href")] = l;
  });
  var groups = document.querySelectorAll(".feat-group");
  if (!groups.length) return;
  var spy = new IntersectionObserver(function(entries){
    entries.forEach(function(e){
      if (e.isIntersecting) {
        Object.keys(links).forEach(function(k){ links[k].classList.remove("active"); });
        var lk = links["#" + e.target.id];
        if (lk) lk.classList.add("active");
      }
    });
  }, { rootMargin: "-80px 0px -65% 0px", threshold: 0 });
  groups.forEach(function(g){ spy.observe(g); });
})();

/* ===== 返回顶部按钮 ===== */
(function(){
  var btn = document.createElement("button");
  btn.className = "back-to-top";
  btn.type = "button";
  btn.setAttribute("aria-label", "返回顶部");
  btn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><polyline points="18 15 12 9 6 15"></polyline></svg>';
  document.body.appendChild(btn);
  btn.addEventListener("click", function(){
    try { window.scrollTo({ top: 0, behavior: "smooth" }); }
    catch(e){ window.scrollTo(0, 0); }
  });
  var ticking = false;
  function update(){
    ticking = false;
    if (window.scrollY > 600) btn.classList.add("show"); else btn.classList.remove("show");
  }
  window.addEventListener("scroll", function(){ if (!ticking){ ticking = true; requestAnimationFrame(update); } }, { passive: true });
  window.addEventListener("resize", update);
  update();
})();

/* ===== 邮件订阅表单 ===== */
(function(){
  var form = document.getElementById("subscribeForm");
  if (!form) return;
  var msgEl = document.getElementById("subscribeMsg");
  form.addEventListener("submit", function(e){
    e.preventDefault();
    var emailEl = document.getElementById("subscribeEmail");
    var phoneEl = document.getElementById("subscribePhone");
    var email = (emailEl && emailEl.value.trim()) || "";
    var phone = (phoneEl && phoneEl.value.trim()) || "";
    if (!email || !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)) {
      showMsg(AIOpsI18n.t("subscribe.invalid"), "var(--warn)");
      return;
    }
    var btn = form.querySelector('button[type="submit"]');
    var origText = btn ? btn.textContent : "";
    if (btn) { btn.disabled = true; }
    var body = {
      email: email,
      phone: phone,
      source: document.referrer || location.href
    };
    fetch("/api/subscribe", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      credentials: "same-origin"
    }).then(function(r) { return r.json().then(function(j) { return { ok: r.ok, j: j }; }); })
      .then(function(res) {
        if (!res.ok) {
          showMsg(AIOpsI18n.t("subscribe.invalid"), "var(--warn)");
          return;
        }
        showMsg(res.j.status === "updated" ? AIOpsI18n.t("subscribe.dup") : AIOpsI18n.t("subscribe.ok"), "var(--ok)");
        form.reset();
      })
      .catch(function() {
        showMsg(AIOpsI18n.t("subscribe.storageErr"), "var(--warn)");
      })
      .finally(function() {
        if (btn) { btn.disabled = false; btn.textContent = origText; }
      });
  });
  function showMsg(text, color) {
    if (!msgEl) return;
    msgEl.textContent = text;
    msgEl.style.color = color || "var(--text)";
    msgEl.style.display = "block";
  }
})();

/* ===== 滚动进度指示器 ===== */
(function(){
  var indicator = document.createElement("div");
  indicator.className = "scroll-progress";
  indicator.style.cssText = "position:fixed;top:0;left:0;height:3px;background:var(--gradient);z-index:200;border-radius:0 3px 3px 0;transition:width 120ms linear;will-change:width;";
  document.body.appendChild(indicator);
  var scrollTicking = false;
  function updateScrollProgress() {
    scrollTicking = false;
    var scrollTop = window.scrollY || document.documentElement.scrollTop;
    var docHeight = document.documentElement.scrollHeight - window.innerHeight;
    var progress = docHeight > 0 ? (scrollTop / docHeight) : 0;
    indicator.style.width = (progress * 100).toFixed(2) + "%";
  }
  window.addEventListener("scroll", function() {
    if (!scrollTicking) {
      scrollTicking = true;
      requestAnimationFrame(updateScrollProgress);
    }
  }, { passive: true });
  updateScrollProgress();
})();

/* ===== CTA 命令一键复制 ===== */
(function(){
  function tkey(k, fallback) {
    try {
      if (window.AIOpsI18n && window.AIOpsI18n.t) {
        var v = window.AIOpsI18n.t(k);
        if (v && v !== k) return v;
      }
    } catch (e) {}
    return fallback;
  }

  function plainTextFromHtml(html) {
    var tmp = document.createElement("div");
    tmp.innerHTML = html;
    return (tmp.textContent || tmp.innerText || "").replace(/\n{3,}/g, "\n\n").trim();
  }

  function wireCopy(cmdEl) {
    if (!cmdEl || cmdEl.dataset.copyWired) return;
    cmdEl.dataset.copyWired = "1";

    var wrap = cmdEl.parentElement;
    if (!wrap || !wrap.classList.contains("cta-cmd-wrap")) {
      wrap = document.createElement("div");
      wrap.className = "cta-cmd-wrap";
      cmdEl.parentNode.insertBefore(wrap, cmdEl);
      wrap.appendChild(cmdEl);
    }

    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "cta-copy-btn";
    btn.setAttribute("data-i18n", "cta.copy");
    btn.textContent = tkey("cta.copy", "复制命令");
    wrap.appendChild(btn);

    btn.addEventListener("click", function() {
      var text = plainTextFromHtml(cmdEl.innerHTML);
      function done() {
        btn.classList.add("copied");
        btn.textContent = tkey("cta.copied", "已复制");
        setTimeout(function() {
          btn.classList.remove("copied");
          btn.textContent = tkey("cta.copy", "复制命令");
        }, 1800);
      }
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done).catch(function() {
          fallbackCopy(text, done);
        });
      } else {
        fallbackCopy(text, done);
      }
    });
  }

  function fallbackCopy(text, done) {
    try {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.style.cssText = "position:fixed;left:-9999px;top:0";
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      document.body.removeChild(ta);
      done();
    } catch (e) {}
  }

  function init() {
    document.querySelectorAll(".cta-cmd").forEach(wireCopy);
  }
  init();
  try { document.addEventListener("lang:changed", function() {
    document.querySelectorAll(".cta-copy-btn").forEach(function(btn) {
      if (!btn.classList.contains("copied")) btn.textContent = tkey("cta.copy", "复制命令");
    });
  }); } catch (e) {}
})();

/* ===== 跳过链接（无障碍） ===== */
(function(){
  if (document.querySelector(".skip-link")) return;
  var a = document.createElement("a");
  a.className = "skip-link";
  a.href = "#main";
  a.textContent = "跳到主要内容";
  document.body.insertBefore(a, document.body.firstChild);
  var main = document.getElementById("main");
  if (!main) {
    var first = document.querySelector("section.hero, section.section, section.page-hero");
    if (first) first.id = "main";
  }
})();

/* ===== 增强用户行为追踪 ===== */
(function(){
  try {
    var path = location.pathname || "/";
    var pageName = path.replace(/^.*\//, "").replace(/\.html$/, "") || "index";
    
    // 全局会话管理（跨页面共享）
    var SESSION_KEY = "aiops_global_session";
    var SESSION_TIMEOUT = 30 * 60 * 1000; // 30分钟
    var now = Date.now();
    var sessionData = null;
    try {
      var stored = localStorage.getItem(SESSION_KEY);
      if (stored) {
        sessionData = JSON.parse(stored);
        if (now - sessionData.lastActivity > SESSION_TIMEOUT) {
          sessionData = null;
        }
      }
    } catch(e) { sessionData = null; }
    
    if (!sessionData) {
      sessionData = {
        id: now + "_" + Math.random().toString(36).substr(2, 9),
        startTime: now,
        pageViews: []
      };
    }
    sessionData.lastActivity = now;
    sessionData.pageViews.push({ page: pageName, ts: now });
    localStorage.setItem(SESSION_KEY, JSON.stringify(sessionData));
    
    var sessionId = sessionData.id;
    var startTime = now;
    var maxScroll = 0;
    var interactions = [];
    
    // 解析 UTM 参数
    var utm = {};
    try {
      var params = new URLSearchParams(location.search);
      ["utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content"].forEach(function(k) {
        var v = params.get(k);
        if (v) utm[k] = v;
      });
    } catch(e) {}
    
    // 解析 User-Agent（增强版：含版本号和设备类型）
    var ua = navigator.userAgent || "";
    var os = "Unknown", osVersion = "";
    var browser = "Unknown", browserVersion = "";
    var deviceType = "Desktop";
    
    // 操作系统检测
    if (ua.indexOf("Windows NT 10") >= 0) { os = "Windows"; osVersion = "10/11"; }
    else if (ua.indexOf("Windows NT 6.3") >= 0) { os = "Windows"; osVersion = "8.1"; }
    else if (ua.indexOf("Windows NT 6") >= 0) { os = "Windows"; osVersion = "7/8"; }
    else if (ua.indexOf("Mac OS X") >= 0) {
      os = "macOS";
      var m = ua.match(/Mac OS X ([\d_]+)/);
      if (m) osVersion = m[1].replace(/_/g, ".");
    }
    else if (ua.indexOf("Android") >= 0) {
      os = "Android";
      var m = ua.match(/Android ([\d.]+)/);
      if (m) osVersion = m[1];
      deviceType = "Mobile";
    }
    else if (ua.indexOf("iPhone") >= 0 || ua.indexOf("iPad") >= 0) {
      os = "iOS";
      var m = ua.match(/OS ([\d_]+)/);
      if (m) osVersion = m[1].replace(/_/g, ".");
      deviceType = ua.indexOf("iPad") >= 0 ? "Tablet" : "Mobile";
    }
    else if (ua.indexOf("Linux") >= 0) { os = "Linux"; }
    
    // 浏览器检测
    if (ua.indexOf("Edg/") >= 0) {
      browser = "Edge";
      var m = ua.match(/Edg\/([\d.]+)/);
      if (m) browserVersion = m[1];
    }
    else if (ua.indexOf("Chrome/") >= 0 && ua.indexOf("Edg") < 0) {
      browser = "Chrome";
      var m = ua.match(/Chrome\/([\d.]+)/);
      if (m) browserVersion = m[1];
    }
    else if (ua.indexOf("Firefox/") >= 0) {
      browser = "Firefox";
      var m = ua.match(/Firefox\/([\d.]+)/);
      if (m) browserVersion = m[1];
    }
    else if (ua.indexOf("Safari/") >= 0 && ua.indexOf("Chrome") < 0) {
      browser = "Safari";
      var m = ua.match(/Version\/([\d.]+)/);
      if (m) browserVersion = m[1];
    }
    
    // 平板检测（非iPad的Android大屏设备）
    if (deviceType === "Desktop" && window.innerWidth <= 1024 && "ontouchstart" in window) {
      deviceType = "Tablet";
    }
    
    var deviceInfo = {
      screen: screen.width + "x" + screen.height,
      viewport: window.innerWidth + "x" + window.innerHeight,
      lang: navigator.language || "unknown",
      platform: navigator.platform || "unknown",
      os: os,
      osVersion: osVersion,
      browser: browser,
      browserVersion: browserVersion,
      deviceType: deviceType,
      ua: ua.substring(0, 150),
      tz: Intl.DateTimeFormat().resolvedOptions().timeZone || "unknown"
    };
    
    // 获取公网IP和地理位置信息
    var geoInfo = {};
    var geoReady = false;
    
    // 先从缓存读取
    try {
      var cachedGeo = localStorage.getItem("aiops_geo_cache");
      if (cachedGeo) {
        var parsed = JSON.parse(cachedGeo);
        // 缓存有效期24小时
        if (Date.now() - parsed._cachedAt < 24 * 60 * 60 * 1000) {
          geoInfo = parsed;
          geoReady = true;
        }
      }
    } catch(e) {}
    
    // 异步获取IP和地理位置（多API降级链）
    var fetchGeo = function() {
      var done = false;
      var finish = function() {
        if (done) return;
        done = true;
        geoReady = true;
        geoInfo._cachedAt = Date.now();
        try { localStorage.setItem("aiops_geo_cache", JSON.stringify(geoInfo)); } catch(e) {}
      };
      
      // API 降级链：每个 API 返回 IP + 地理位置，失败则尝试下一个
      var apis = [
        // 1. ip-api.com (CORS 支持好，45次/分钟免费，返回完整地理信息)
        {
          url: "http://ip-api.com/json/?fields=status,message,country,regionName,city,org,query",
          parse: function(data) {
            if (data.status === "success") {
              geoInfo.ip = data.query || "";
              geoInfo.country = data.country || "";
              geoInfo.region = data.regionName || "";
              geoInfo.city = data.city || "";
              geoInfo.org = data.org || "";
              return true;
            }
            return false;
          }
        },
        // 2. ipapi.co (备选，有 CORS 支持，1000次/天免费)
        {
          url: "https://ipapi.co/json/",
          parse: function(data) {
            if (data.error) return false;
            geoInfo.ip = data.ip || "";
            geoInfo.country = data.country_name || "";
            geoInfo.region = data.region || "";
            geoInfo.city = data.city || "";
            geoInfo.org = data.org || "";
            return true;
          }
        },
        // 3. ip.sb (仅返回 IP，无地理信息)
        {
          url: "https://api.ip.sb/geoip",
          parse: function(data) {
            if (data.ip) {
              geoInfo.ip = data.ip || "";
              geoInfo.country = data.country || "";
              geoInfo.region = data.region || "";
              geoInfo.city = data.city || "";
              geoInfo.org = data.isp || "";
              return true;
            }
            return false;
          }
        }
      ];
      
      var tryApi = function(index) {
        if (index >= apis.length || done) {
          finish();
          return;
        }
        var api = apis[index];
        var xhr = new XMLHttpRequest();
        xhr.open("GET", api.url, true);
        xhr.timeout = 5000;
        xhr.onreadystatechange = function() {
          if (xhr.readyState === 4) {
            if (xhr.status === 200) {
              try {
                var data = JSON.parse(xhr.responseText);
                if (api.parse(data)) {
                  finish();
                  return;
                }
              } catch(e) {}
            }
            // 失败，尝试下一个 API
            tryApi(index + 1);
          }
        };
        xhr.onerror = function() { tryApi(index + 1); };
        xhr.ontimeout = function() { tryApi(index + 1); };
        xhr.send();
      };
      
      tryApi(0);
    };
    
    if (!geoReady) {
      fetchGeo();
    }
    
    // 追踪滚动深度（passive 优化，不阻塞滚动）
    var scrollTicking = false;
    window.addEventListener("scroll", function() {
      if (!scrollTicking) {
        scrollTicking = true;
        requestAnimationFrame(function() {
          var docH = document.documentElement.scrollHeight - window.innerHeight;
          var scrollPercent = docH > 0 ? Math.round((window.scrollY / docH) * 100) : 0;
          if (scrollPercent > maxScroll) maxScroll = scrollPercent;
          scrollTicking = false;
        });
      }
    }, { passive: true });
    
    // 全站点击埋点：识别元素类型
    function classifyClick(target) {
      if (!target) return null;
      var tag = target.tagName || "";
      var cls = (typeof target.className === "string") ? target.className : "";
      var id = target.id || "";
      var text = (target.textContent || "").trim().substring(0, 40);
      var href = target.getAttribute("href") || "";
      var dataI18n = target.getAttribute("data-i18n") || "";
      
      // 导航栏链接
      if (target.closest(".nav-links")) {
        if (target.closest(".nav-dropdown-menu")) return { type: "nav_dropdown", text: text, href: href };
        return { type: "nav_link", text: text, href: href };
      }
      // 导航CTA
      if (cls.indexOf("nav-cta") >= 0 || target.closest(".nav-cta")) return { type: "nav_cta", text: text, href: href };
      // 主题切换
      if (id === "themeToggle" || target.closest("#themeToggle")) return { type: "theme_toggle", text: "theme" };
      // 语言切换
      if (dataI18n.indexOf("lang") >= 0 || cls.indexOf("lang-switch") >= 0 || target.closest("[data-i18n*='lang']")) return { type: "lang_switch", text: text };
      // 移动端菜单
      if (cls.indexOf("nav-toggle") >= 0 || target.closest(".nav-toggle")) return { type: "mobile_menu", text: "menu" };
      // Hero区CTA
      if (target.closest(".hero") && (cls.indexOf("btn-primary") >= 0 || cls.indexOf("btn-secondary") >= 0 || cls.indexOf("hero-cta") >= 0)) return { type: "hero_cta", text: text, href: href };
      // 功能卡片
      if (target.closest(".feature-card") || target.closest(".feat-card")) return { type: "feature_card", text: text };
      // 定价卡片/套餐按钮
      if (target.closest(".pricing-card") || target.closest(".price-card")) return { type: "pricing_card", text: text };
      // 案例卡片
      if (target.closest(".case-card") || target.closest(".cases-card")) return { type: "case_card", text: text };
      // FAQ折叠
      if (target.closest(".faq-item") || target.closest(".faq-question") || cls.indexOf("faq") >= 0) return { type: "faq_toggle", text: text };
      // 交叉引导链接
      if (target.closest(".cross-sell") || target.closest(".next-pages") || cls.indexOf("cross") >= 0) return { type: "cross_sell", text: text, href: href };
      // 面包屑
      if (target.closest(".breadcrumb") || target.closest(".breadcrumbs")) return { type: "breadcrumb", text: text, href: href };
      // 页脚链接
      if (target.closest("footer") || target.closest(".footer")) return { type: "footer_link", text: text, href: href };
      // 联系表单提交
      if (target.closest("#contactForm") && (tag === "BUTTON" || cls.indexOf("btn") >= 0)) return { type: "contact_submit", text: text };
      // 订阅表单提交
      if (target.closest("#subscribeForm") && (tag === "BUTTON" || cls.indexOf("btn") >= 0)) return { type: "subscribe_submit", text: text };
      // 通用CTA按钮
      if (cls.indexOf("btn-primary") >= 0 || cls.indexOf("btn-secondary") >= 0) return { type: "cta_button", text: text, href: href };
      // 返回顶部
      if (cls.indexOf("back-to-top") >= 0) return { type: "back_to_top", text: "top" };
      // 锚点链接
      if (href && href.charAt(0) === "#") return { type: "anchor_link", text: text, href: href };
      // 外部链接
      if (href && href.indexOf("http") === 0 && href.indexOf(location.origin) < 0) return { type: "external_link", text: text, href: href };
      
      return null;
    }
    
    // 全局点击监听
    document.addEventListener("click", function(e) {
      var target = e.target.closest("a, button, [role='button'], .faq-item, .faq-question, [data-i18n*='lang']");
      if (!target) return;
      var info = classifyClick(target);
      if (info) {
        interactions.push({
          type: info.type,
          text: info.text || "",
          href: info.href || "",
          ts: Date.now() - startTime
        });
      }
    });
    
    // 页面离开时保存访问记录（优先 SQLite API，失败时回退 localStorage）
    var saved = false;
    var postVisit = function(payload) {
      var raw = JSON.stringify(payload);
      try {
        if (navigator.sendBeacon) {
          var blob = new Blob([raw], { type: "application/json" });
          if (navigator.sendBeacon("/api/visit", blob)) return true;
        }
      } catch (e) {}
      try {
        fetch("/api/visit", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: raw,
          keepalive: true,
          credentials: "same-origin"
        });
        return true;
      } catch (e2) {
        return false;
      }
    };
    var saveVisit = function() {
      if (saved) return;
      saved = true;
      
      var duration = Math.round((Date.now() - startTime) / 1000);
      if (duration < 3) return;
      
      var record = {
        page: pageName,
        path: path,
        ts: new Date().toISOString(),
        enterTs: startTime,
        exitTs: Date.now(),
        ref: document.referrer || "",
        duration: duration,
        scroll: maxScroll,
        interactions: interactions.slice(0, 50),
        device: deviceInfo,
        session: sessionId,
        utm: utm,
        geo: geoInfo
      };
      if (postVisit(record)) return;

      // API 不可用时回退本地缓存（管理后台可一键导入）
      var visits = [];
      try { visits = JSON.parse(localStorage.getItem("aiops_page_views") || "[]"); } catch(e) { visits = []; }
      visits.push(record);
      if (visits.length > 2000) visits = visits.slice(-2000);
      try {
        localStorage.setItem("aiops_page_views", JSON.stringify(visits));
      } catch(storageErr) {
        if (visits.length > 100) {
          try {
            localStorage.setItem("aiops_page_views", JSON.stringify(visits.slice(-500)));
          } catch(e2) {}
        }
      }
    };
    
    window.addEventListener("beforeunload", saveVisit);
    document.addEventListener("visibilitychange", function() {
      if (document.visibilityState === "hidden") saveVisit();
    });
    
    // 空闲超时保存（防抖：仅对有意义交互重置，scroll/mousemove 节流）
    var idleTimer;
    var lastIdleReset = 0;
    var resetIdle = function(e) {
      var now = Date.now();
      // scroll/mousemove 至少间隔2秒才重置空闲计时器，避免高频触发
      if (e && (e.type === "scroll" || e.type === "mousemove")) {
        if (now - lastIdleReset < 2000) return;
      }
      lastIdleReset = now;
      clearTimeout(idleTimer);
      idleTimer = setTimeout(saveVisit, SESSION_TIMEOUT);
    };
    ["mousemove", "keydown", "scroll", "click"].forEach(function(evt) {
      document.addEventListener(evt, resetIdle, evt === "scroll" ? { passive: true } : false);
    });
    resetIdle();
    
  } catch(e) {}
})();

/* ===== 联系表单处理（增强版：防重复提交 + 长度限制 + i18n） ===== */
(function(){
  var form = document.getElementById("contactForm");
  if (!form) return;
  var msgEl = document.getElementById("contactMsg");
  var submitting = false;
  
  // 输入长度限制
  var limits = { contactName: 50, contactEmail: 100, contactPhone: 20, contactMessage: 500 };
  Object.keys(limits).forEach(function(id) {
    var el = document.getElementById(id);
    if (el) el.setAttribute("maxlength", limits[id]);
  });
  
  // 实时验证反馈
  var emailEl = document.getElementById("contactEmail");
  var phoneEl = document.getElementById("contactPhone");
  function validateField(el, regex, errId) {
    if (!el) return;
    el.addEventListener("blur", function() {
      var val = el.value.trim();
      if (val && !regex.test(val)) {
        el.style.borderColor = "var(--warn, #f59e0b)";
        el.title = el.dataset.errMsg || "格式不正确";
      } else {
        el.style.borderColor = "";
        el.title = "";
      }
    });
    el.addEventListener("input", function() {
      if (el.style.borderColor) {
        el.style.borderColor = "";
        el.title = "";
      }
    });
  }
  validateField(emailEl, /^[^@\s]+@[^@\s]+\.[^@\s]+$/);
  validateField(phoneEl, /^\d{7,15}$/);
  
  // 获取 i18n 消息
  function t(key, fallback) {
    try {
      if (window.AIOpsI18n && window.AIOpsI18n.t) {
        var v = window.AIOpsI18n.t(key);
        if (v && v !== key) return v;
      }
    } catch(e) {}
    return fallback;
  }
  
  form.addEventListener("submit", function(e){
    e.preventDefault();
    // 防重复提交
    if (submitting) return;
    
    var nameEl = document.getElementById("contactName");
    var msgBoxEl = document.getElementById("contactMessage");
    var name = (nameEl && nameEl.value.trim()) || "";
    var email = (emailEl && emailEl.value.trim()) || "";
    var phone = (phoneEl && phoneEl.value.trim()) || "";
    var message = (msgBoxEl && msgBoxEl.value.trim()) || "";
    
    // 验证
    if (!email || !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)) {
      showContactMsg(t("contact.invalidEmail", "请输入有效的邮箱地址"), "var(--warn, #f59e0b)");
      if (emailEl) { emailEl.focus(); emailEl.style.borderColor = "var(--warn, #f59e0b)"; }
      return;
    }
    if (!phone || !/^\d{7,15}$/.test(phone.replace(/[\s\-\+]/g, ""))) {
      showContactMsg(t("contact.invalidPhone", "请输入有效的手机号码"), "var(--warn, #f59e0b)");
      if (phoneEl) { phoneEl.focus(); phoneEl.style.borderColor = "var(--warn, #f59e0b)"; }
      return;
    }
    if (name.length > 50 || message.length > 500) {
      showContactMsg(t("contact.tooLong", "输入内容超出长度限制"), "var(--warn, #f59e0b)");
      return;
    }
    
    submitting = true;
    var btn = form.querySelector('button[type="submit"]');
    var origText = btn ? btn.textContent : "";
    if (btn) { btn.disabled = true; btn.textContent = t("contact.submitting", "提交中..."); }

    fetch("/api/contact", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({
        name: name,
        email: email,
        phone: phone.replace(/[\s\-\+]/g, ""),
        message: message,
        source: location.href
      })
    }).then(function(r) { return r.json().then(function(j) { return { ok: r.ok, j: j }; }); })
      .then(function(res) {
        if (!res.ok) {
          var err = (res.j && res.j.error) || "";
          if (err === "invalid_email") showContactMsg(t("contact.invalidEmail", "请输入有效的邮箱地址"), "var(--warn, #f59e0b)");
          else if (err === "invalid_phone") showContactMsg(t("contact.invalidPhone", "请输入有效的手机号码"), "var(--warn, #f59e0b)");
          else showContactMsg(t("contact.storageErr", "提交失败，请确认已用 python website/serve.py 启动本地服务"), "var(--warn, #f59e0b)");
          return;
        }
        showContactMsg(
          res.j.status === "updated" ? t("contact.updated", "信息已更新，感谢您的关注！") : t("contact.success", "提交成功，我们会尽快与您联系！"),
          "var(--ok, #10b981)"
        );
        form.reset();
      })
      .catch(function() {
        showContactMsg(t("contact.storageErr", "提交失败，请确认已用 python website/serve.py 启动本地服务"), "var(--warn, #f59e0b)");
      })
      .finally(function() {
        submitting = false;
        if (btn) { btn.disabled = false; btn.textContent = origText; }
      });
  });
  function showContactMsg(text, color) {
    if (!msgEl) return;
    msgEl.textContent = text;
    msgEl.style.color = color || "var(--text)";
    msgEl.style.display = "block";
    // 5秒后自动隐藏
    clearTimeout(showContactMsg._timer);
    showContactMsg._timer = setTimeout(function() { msgEl.style.display = "none"; }, 5000);
  }
})();

})();
