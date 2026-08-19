// AIOps Service Worker
// Cache: app shell on install, stale-while-revalidate for static, network-only for API.
// Offline: cached shell + navigation fallback to "/" so the UI shows even offline.

// 缓存名带版本：服务端在返回 /sw.js 时把 __AIOPS_VERSION__ 换成当前 appVersion，
// 于是**每次发版这个文件的字节都不同** —— 浏览器据此认定 SW 有新版本并触发更新，
// 旧版本的缓存也随之在 activate 里被清掉。此前这里是写死的 v0.19.54，意味着 SW 一旦
// 装上就再也不会更新，它自己的逻辑缺陷也永远修不掉。
const CACHE = "aiops-classic-__AIOPS_VERSION__";
// 只清理**本控制台自己**的缓存。caches.keys() 是 origin 级的，不区分 SW 作用域：
// 原来的写法「删掉所有 key !== CACHE 的缓存」会连 /v2 控制台的缓存一起删掉，反之亦然
// ——两个控制台互相把对方的离线能力清空。前缀隔离后各扫各的。
const CACHE_PREFIX = "aiops-classic-";
const LEGACY_CACHES = ["AIOps-v0.19.54-csrf-proxy"];
const SHELL = ["/", "/theme-init.js", "/manifest.json", "/icon.svg"];

self.addEventListener("install", e => {
  e.waitUntil(
    caches.open(CACHE).then(c => c.addAll(SHELL)).then(() => self.skipWaiting())
  );
});

self.addEventListener("activate", e => {
  e.waitUntil(
    caches.keys().then(keys =>
      Promise.all(
        keys
          .filter(k => k !== CACHE && (k.startsWith(CACHE_PREFIX) || LEGACY_CACHES.includes(k)))
          .map(k => caches.delete(k))
      )
    ).then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", e => {
  const req = e.request;
  if (req.method !== "GET") return;

  const url = new URL(req.url);
  if (url.pathname.startsWith("/api/")) return;

  // 页面 / JS / CSS：始终走网络，避免 UI 微调被旧缓存卡住；离线时再回落缓存。
  const p = url.pathname;
  if (req.mode === "navigate" || p === "/" || p.endsWith(".js") || p.endsWith(".css")) {
    e.respondWith(
      fetch(req).catch(() => caches.match(req).then(c => c || caches.match("/")))
    );
    return;
  }

  // Other static assets (icons / manifest / fonts): stale-while-revalidate.
  e.respondWith(
    caches.open(CACHE).then(async cache => {
      const cached = await cache.match(req);
      const fetchPromise = fetch(req).then(res => {
        if (res && res.status === 200) cache.put(req, res.clone());
        return res;
      }).catch(() => cached);
      return cached || fetchPromise;
    })
  );
});
