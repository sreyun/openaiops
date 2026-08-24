"use strict";
const fs = require("fs");
const path = require("path");

// Load by eval-like: extract T from files via Function after concatenating assign hooks
const dir = __dirname;
const i18nSrc = fs.readFileSync(path.join(dir, "js/i18n.js"), "utf8");
const extraSrc = fs.readFileSync(path.join(dir, "js/i18n-extra.js"), "utf8");
const koSrc = fs.readFileSync(path.join(dir, "js/i18n-ko.js"), "utf8");

function extractKeysFromLangBlocks(src, lang) {
  const keys = new Set();
  // Match "lang": { ... } blocks naively by brace depth after match
  const marker = '"' + lang + '"';
  let i = 0;
  while (true) {
    const idx = src.indexOf(marker, i);
    if (idx < 0) break;
    const brace = src.indexOf("{", idx + marker.length);
    if (brace < 0) break;
    let depth = 0;
    let j = brace;
    for (; j < src.length; j++) {
      const c = src[j];
      if (c === "{") depth++;
      else if (c === "}") {
        depth--;
        if (depth === 0) {
          j++;
          break;
        }
      } else if (c === '"' || c === "'") {
        const q = c;
        j++;
        while (j < src.length) {
          if (src[j] === "\\") {
            j += 2;
            continue;
          }
          if (src[j] === q) break;
          j++;
        }
      }
    }
    const block = src.slice(brace, j);
    for (const m of block.matchAll(/"([^"\\]+)"\s*:/g)) {
      if (!["zh-CN", "zh-TW", "en", "ja", "ko"].includes(m[1])) keys.add(m[1]);
    }
    i = j;
  }
  return keys;
}

const langs = ["zh-CN", "zh-TW", "en", "ja", "ko"];
const allSrc = i18nSrc + "\n" + extraSrc + "\n" + koSrc;
const byLang = {};
for (const lang of langs) byLang[lang] = extractKeysFromLangBlocks(allSrc, lang);

const base = byLang["zh-CN"];
console.log("Key counts:", Object.fromEntries(langs.map((l) => [l, byLang[l].size])));
for (const lang of langs) {
  if (lang === "zh-CN") continue;
  const missing = [...base].filter((k) => !byLang[lang].has(k)).sort();
  const extra = [...byLang[lang]].filter((k) => !base.has(k)).sort();
  console.log("\n==", lang, "missing vs zh-CN:", missing.length);
  if (missing.length) console.log(missing.slice(0, 40).join("\n") + (missing.length > 40 ? "\n..." : ""));
  console.log("==", lang, "extra vs zh-CN:", extra.length);
  if (extra.length) console.log(extra.slice(0, 20).join("\n") + (extra.length > 20 ? "\n..." : ""));
}
