/* verify_site.js — jsdom 端到端检查：加载真实 HTML，运行脚本，检查渲染结果。
   用法: node verify_site.js <file> <lang>
   检查: (1) 是否存在未翻译的 raw key 文本; (2) 关键内容是否正确渲染。 */
const { JSDOM } = require('jsdom');
const path = require('path');

function run(file, lang) {
  return new Promise((resolve) => {
    const abs = path.resolve(file).replace(/\\/g, '/');
    JSDOM.fromFile(file, {
      runScripts: 'dangerously',
      resources: 'usable',
      url: 'file://' + abs + '?lang=' + lang,
      pretendToBeVisual: true,
      beforeParse: function (window) {
        window.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} };
        window.matchMedia = window.matchMedia || function () { return { matches: false, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {} }; };
      }
    }).then(function (dom) {
      setTimeout(function () {
        const doc = dom.window.document;
        const html = doc.documentElement.outerHTML;
        const issues = [];
        // 真实泄漏：带 data-i18n 的元素其可见文本仍等于 key 本身
        doc.querySelectorAll('[data-i18n]').forEach(function (el) {
          const key = el.getAttribute('data-i18n');
          if (el.textContent.trim() === key) issues.push('UNTRANSLATED:' + key);
        });
        // 动态渲染内容里若出现 key 片段（如 secBackground）也视为泄漏
        ['secBackground','secPain','secSolution','bestFor','footNote','recommendLabel']
          .forEach(function (k) { if (html.indexOf('>' + k + '<') >= 0) issues.push('RAWKEY_IN_TEXT:' + k); });
        // 韩语页面：可见文本不应含汉字（CJK 统一表意文字），否则为回退到中文。
        // 排除 #langSelect（语言切换器按各语言原生写法展示，如“简体中文”属正常）。
        if (lang === 'ko') {
          const clone = doc.body.cloneNode(true);
          const sw = clone.querySelector('#langSelect');
          if (sw) sw.remove();
          const text = clone.textContent || '';
          const han = text.match(/[一-鿿]/g);
          if (han) issues.push('CJK_LEAK(count=' + han.length + ')');
        }
        // 语言相关正向检查
        const pos = {};
        if (path.basename(file) === 'index.html') {
          const units = Array.from(doc.querySelectorAll('.hero-stat .unit')).map(e => e.textContent.trim());
          pos.heroUnits = units;
          // 4 语言单位期望
          const expect = { 'en': ['min','platforms','%'], 'zh-CN': ['分钟','平台','%'], 'zh-TW': ['分鐘','平台','%'], 'ja': ['分','プラットフォーム','%'], 'ko': ['분','플랫폼','%'] }[lang];
          if (expect && JSON.stringify(units) !== JSON.stringify(expect)) issues.push('HERO_UNITS_MISMATCH got=' + JSON.stringify(units) + ' exp=' + JSON.stringify(expect));
        }
        if (path.basename(file) === 'cases.html') {
          const list = doc.getElementById('caseList');
          pos.caseCount = list ? list.querySelectorAll('.case-card').length : 0;
          pos.hasNarrative = list ? list.querySelectorAll('.case-narrative').length : 0;
          pos.hasBg = list ? list.querySelectorAll('.case-block').length : 0;
          if (pos.caseCount !== 5) issues.push('CASE_COUNT=' + pos.caseCount);
          if (pos.hasBg < 15) issues.push('NARRATIVE_BLOCKS=' + pos.hasBg);
        }
        if (path.basename(file) === 'pricing.html') {
          const cards = doc.getElementById('priceCards');
          pos.planCount = cards ? cards.querySelectorAll('.price-card').length : 0;
          pos.hasBest = cards ? cards.querySelectorAll('.price-best').length : 0;
          pos.hasFoot = doc.getElementById('priceCompare') ? doc.getElementById('priceCompare').querySelectorAll('.price-foot').length : 0;
          if (pos.planCount !== 3) issues.push('PLAN_COUNT=' + pos.planCount);
          if (pos.hasBest !== 3) issues.push('BESTFOR_COUNT=' + pos.hasBest);
        }
        resolve({ file: path.basename(file), lang: lang, issues: issues, pos: pos });
      }, 2500);
    }).catch(function (e) { resolve({ file: path.basename(file), lang: lang, error: String(e) }); });
  });
}

const args = process.argv.slice(2);
const file = args[0]; const lang = args[1];
run(file, lang).then(function (r) {
  console.log(JSON.stringify(r, null, 2));
});
