/* restructure_nav.js — 一次性脚本：把全部 8 页的 nav 块从「5 项下拉 + FAQ/联系」改为「3 项下拉（功能/方案/对比）+ 独立 定价/案例/FAQ/联系」，对应页设 class="active" */
const fs = require('fs');
const path = require('path');
const pages = ['index','features','solutions','comparison','faq','contact','pricing','cases'];

function buildNav(activePage) {
  return `<nav class="navbar">
  <div class="nav-inner">
    <a href="index.html" class="nav-logo">
      <span class="logo-icon">AI</span>
      <span>AIOps</span>
    </a>
    <div class="nav-links">
      <a href="index.html"${activePage==='index'?' class="active"':''} data-i18n="nav.home">首页</a>
      <div class="nav-dropdown" id="navProduct">
        <button type="button" class="nav-dropdown-toggle" aria-haspopup="true" aria-expanded="false"><span data-i18n="nav.product">产品</span><span class="caret">▾</span></button>
        <div class="nav-dropdown-menu">
          <a href="features.html"${activePage==='features'?' class="active"':''} data-i18n="nav.features">功能详情</a>
          <a href="solutions.html"${activePage==='solutions'?' class="active"':''} data-i18n="nav.solutions">解决方案</a>
          <a href="comparison.html"${activePage==='comparison'?' class="active"':''} data-i18n="nav.comparison">产品对比</a>
        </div>
      </div>
      <a href="pricing.html"${activePage==='pricing'?' class="active"':''} data-i18n="nav.pricing">定价方案</a>
      <a href="cases.html"${activePage==='cases'?' class="active"':''} data-i18n="nav.cases">客户案例</a>
      <a href="faq.html"${activePage==='faq'?' class="active"':''} data-i18n="nav.faq">常见问题</a>
      <a href="contact.html"${activePage==='contact'?' class="active"':''} data-i18n="nav.contact">联系我们</a>
    </div>
    <a href="https://github.com/sreyun/aiops-monitor" target="_blank" class="nav-cta" data-i18n="nav.cta">免费部署</a>
    <button class="nav-toggle">☰</button>
  </div>
</nav>`;
}

const re = /<nav class="navbar">[\s\S]*?<\/nav>/;
let ok = 0, bad = 0;
for (const p of pages) {
  const f = p + '.html';
  let html = fs.readFileSync(f, 'utf8');
  const next = buildNav(p);
  if (!re.test(html)) { console.log('SKIP', f); bad++; continue; }
  html = html.replace(re, next);
  fs.writeFileSync(f, html, 'utf8');
  console.log('OK', f);
  ok++;
}
console.log('ok=' + ok + ' bad=' + bad);
