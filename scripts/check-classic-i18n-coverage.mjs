#!/usr/bin/env node
/**
 * 经典控制台（cmd/server/web/js/*.js）的 i18n 覆盖度量。
 *
 * 经典版是**默认出厂界面**，但它的三语化只做了一半：字典里有 3000+ 条，源码里
 * 还留着 2800 条写死的中文。差别在于——写死的那些，英文/繁体用户看到的就是简体中文。
 * 前端 /v2 有 check:hardcoded-copy 卡着，经典版一直没有对应的尺子，于是这个数字
 * 既没人知道、也没人盯着它降。
 *
 * 这个脚本**只报数不拦门**（存量太大，一上来就设成硬门禁只会被 --no-verify 绕过）。
 * 用法：
 *   node scripts/check-classic-i18n-coverage.mjs            # 打印总数与 Top 文件
 *   node scripts/check-classic-i18n-coverage.mjs --max 2799 # 超过基线才退出码 1
 *
 * 判定口径：字符串字面量里含中日韩汉字，且**不是** t()/xxT() 的兜底参数；
 * 模板串（含 ${}）跳过——里面的中文基本都是兜底文案，单独数会虚高。
 */
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const DIR = "cmd/server/web/js";
const HAN = /[一-鿿]/;
// t("key", "兜底") / hsT("key", "兜底") / I18N.t("key", "兜底")
const FALLBACK = /\b(?:I18N\.t|[A-Za-z]{0,6}[tT])\(\s*(["'])[^"']+\1\s*,\s*/g;

/** 扫出源码里的字符串字面量，跳过注释。 */
function literals(src) {
  const out = [];
  for (let i = 0; i < src.length; ) {
    const c = src[i];
    if (c === "/" && src[i + 1] === "/") {
      const j = src.indexOf("\n", i);
      i = j < 0 ? src.length : j;
      continue;
    }
    if (c === "/" && src[i + 1] === "*") {
      const j = src.indexOf("*/", i + 2);
      i = j < 0 ? src.length : j + 2;
      continue;
    }
    if (c === '"' || c === "'" || c === "`") {
      let j = i + 1;
      while (j < src.length) {
        if (src[j] === "\\") { j += 2; continue; }
        if (src[j] === c) break;
        j++;
      }
      out.push({ quote: c, body: src.slice(i + 1, j) });
      i = j + 1;
      continue;
    }
    i++;
  }
  return out;
}

const perFile = [];
let total = 0;
for (const name of readdirSync(DIR).filter((f) => f.endsWith(".js")).sort()) {
  const src = readFileSync(join(DIR, name), "utf8").replace(FALLBACK, "T(FB");
  let n = 0;
  const sample = [];
  for (const { quote, body } of literals(src)) {
    if (quote === "`" || body.includes("${")) continue;
    if (!HAN.test(body)) continue;
    n++;
    if (sample.length < 3) sample.push(body.slice(0, 32));
  }
  if (n > 0) perFile.push({ name, n, sample });
  total += n;
}

perFile.sort((a, b) => b.n - a.n);
console.log(`经典控制台写死的中文字面量：${total} 条，分布在 ${perFile.length} 个文件`);
for (const f of perFile.slice(0, 12)) {
  console.log(`  ${String(f.n).padStart(5)}  ${f.name}   例：${f.sample.join(" / ")}`);
}

const maxArg = process.argv.indexOf("--max");
if (maxArg > -1) {
  const max = Number(process.argv[maxArg + 1]);
  if (Number.isFinite(max) && total > max) {
    console.error(`\n超出基线：${total} > ${max}。新增界面文案请走 I18N.t()，不要写死中文。`);
    process.exit(1);
  }
  console.log(`\n未超出基线（${total} <= ${max}）。`);
}
