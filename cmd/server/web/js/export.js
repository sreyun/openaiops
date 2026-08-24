// export.js — 通用文档导出：Markdown / Excel(.xlsx) / Word(.docx) / PDF
//
// 设计：调用方只构造一份中性的「文档模型」，这里负责四种落地格式。
// 任何面板都能复用（目前硬件详情在用），不必各写一套导出。
//
//   {
//     title:    "标题",
//     subtitle: "副标题（可选）",
//     meta:     [[键, 值], ...],                      // 摘要键值对
//     narrative:"分析正文（可选，支持 Markdown 原文留存）",
//     kpis:     [[键, 值], ...],                      // PDF 顶部 KPI（可选）
//     sections: [{ title, columns:[...], rows:[[...]] }, ...]
//   }
//
// 为什么不引第三方库（SheetJS / jsPDF / docx.js）：
//   服务端 CSP 是 script-src 'self'，且本项目坚持零外链。所以 xlsx/docx 这里
//   直接手写 OOXML + 一个最小 ZIP 打包器；ZIP 用 STORE(不压缩)，从而完全
//   省掉 deflate 实现——办公软件对不压缩的 OOXML 一样正常打开。
//
// 为什么 PDF 走浏览器打印而不是自己生成：
//   PDF 要渲染中文必须内嵌 CJK 字体（动辄数 MB，且要做 TTF 子集化）；不内嵌
//   而依赖阅读器的 Adobe-GB1 字体在现代查看器上普遍显示成空白/豆腐块。
//   交给浏览器打印 → 系统字体渲染，中文一定正确，用户在弹窗里选「另存为 PDF」。

/* ============================ 通用工具 ============================ */

// BMC 偶尔会吐出控制字符。混进 XML 会让 Excel/Word 直接判定文件损坏拒绝打开；
// 落到 .md 里虽不致命，但 NUL 出现在文本文件里同样不像话——所以统一先洗一道。
const expClean = s => String(s == null ? "" : s).replace(/[\x00-\x08\x0B\x0C\x0E-\x1F]/g, "");

const expEscXml = s => expClean(s)
  .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
  .replace(/"/g, "&quot;").replace(/'/g, "&apos;");

function expDownload(blob, filename) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  // 立刻 revoke 会让部分浏览器的下载中途失败，挪到下一轮事件循环。
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// 文件名里的非法字符（Windows: \/:*?"<>| ）一律换成下划线，
// 否则 a.download 会被浏览器悄悄改名甚至丢掉扩展名。
const expSafeName = s => String(s || "export")
  .replace(/[\\/:*?"<>|]/g, "_").replace(/\s+/g, "_").slice(0, 120);

const expStamp = () => new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-");

/* ---------------------------- CSV ----------------------------
 * xlsx 走 t="inlineStr"，单元格天然是字面量；CSV 没有这层保护——
 * Excel / WPS / LibreOffice / Numbers 打开时，以 = + - @（以及解析前会被剥掉的
 * 前导 Tab / CR）开头的格子按公式求值，`=cmd|'/c calc'!A1` 这类载荷会在打开
 * 报表的人机器上落地。而这些格子里装的正是 Agent 与网络侧来的文本：主机名、
 * 日志正文、告警内容、NetFlow 的反查域名与 WHOIS 组织名——全是外部可影响的。
 *
 * 判据与新版控制台 src/shared/export.ts 的 rowsToCSV、Android 的 HyperVExport
 * 保持一致：只有纯数字（"-12"、"+3.5"）放行，否则数值列会被单引号毁掉。
 */
const expCsvNeutralize = s => {
  const v = String(s == null ? "" : s);
  if (!/^[=+\-@\t\r]/.test(v)) return v;
  if (/^[+-]?\d+(\.\d+)?$/.test(v)) return v;
  return "'" + v;
};

/** 一格 CSV：先中和公式，再按 RFC 4180 加引号。所有导出都必须走这里。 */
const expCsvCell = s => `"${expCsvNeutralize(s).replace(/"/g, '""')}"`;

/** 表头 + 数据行 → CSV 文本（CRLF 行尾，Excel 最省事）。 */
const expRowsToCsv = (head, rows) =>
  [head.map(expCsvCell).join(",")]
    .concat(rows.map(r => r.map(expCsvCell).join(",")))
    .join("\r\n");

/** UTF-8 BOM：不带 BOM 的中文 CSV 在 Excel 里必然乱码。 */
const EXP_CSV_BOM = "\uFEFF";

/* ============================ 最小 ZIP（STORE） ============================ */

function expCrc32(u8) {
  let table = expCrc32._t;
  if (!table) {
    table = expCrc32._t = new Uint32Array(256);
    for (let n = 0; n < 256; n++) {
      let c = n;
      for (let k = 0; k < 8; k++) c = (c & 1) ? (0xEDB88320 ^ (c >>> 1)) : (c >>> 1);
      table[n] = c >>> 0;
    }
  }
  let c = 0xFFFFFFFF;
  for (let i = 0; i < u8.length; i++) c = table[(c ^ u8[i]) & 0xFF] ^ (c >>> 8);
  return (c ^ 0xFFFFFFFF) >>> 0;
}

// files: [{name, data: Uint8Array}] → Blob(application/zip)
function expZip(files) {
  const enc = new TextEncoder();
  const parts = [], central = [];
  let offset = 0;

  const d = new Date();
  const dosTime = ((d.getHours() << 11) | (d.getMinutes() << 5) | (d.getSeconds() >> 1)) & 0xFFFF;
  const dosDate = (((d.getFullYear() - 1980) << 9) | ((d.getMonth() + 1) << 5) | d.getDate()) & 0xFFFF;

  for (const f of files) {
    const nameB = enc.encode(f.name);
    const crc = expCrc32(f.data);
    const lh = new Uint8Array(30 + nameB.length);
    const v = new DataView(lh.buffer);
    v.setUint32(0, 0x04034b50, true);  // 本地文件头签名
    v.setUint16(4, 20, true);          // version needed
    v.setUint16(6, 0x0800, true);      // flag: 文件名为 UTF-8
    v.setUint16(8, 0, true);           // method 0 = STORE
    v.setUint16(10, dosTime, true);
    v.setUint16(12, dosDate, true);
    v.setUint32(14, crc, true);
    v.setUint32(18, f.data.length, true); // 压缩后大小 == 原始大小
    v.setUint32(22, f.data.length, true);
    v.setUint16(26, nameB.length, true);
    v.setUint16(28, 0, true);          // extra len
    lh.set(nameB, 30);
    parts.push(lh, f.data);
    central.push({ nameB, crc, size: f.data.length, offset });
    offset += lh.length + f.data.length;
  }

  const cdStart = offset;
  for (const c of central) {
    const ch = new Uint8Array(46 + c.nameB.length);
    const v = new DataView(ch.buffer);
    v.setUint32(0, 0x02014b50, true);  // 中央目录签名
    v.setUint16(4, 20, true);          // version made by
    v.setUint16(6, 20, true);          // version needed
    v.setUint16(8, 0x0800, true);
    v.setUint16(10, 0, true);
    v.setUint16(12, dosTime, true);
    v.setUint16(14, dosDate, true);
    v.setUint32(16, c.crc, true);
    v.setUint32(20, c.size, true);
    v.setUint32(24, c.size, true);
    v.setUint16(28, c.nameB.length, true);
    v.setUint32(42, c.offset, true);   // 本地头偏移
    ch.set(c.nameB, 46);
    parts.push(ch);
    offset += ch.length;
  }

  const eocd = new Uint8Array(22);
  const v = new DataView(eocd.buffer);
  v.setUint32(0, 0x06054b50, true);
  v.setUint16(8, central.length, true);
  v.setUint16(10, central.length, true);
  v.setUint32(12, offset - cdStart, true);
  v.setUint32(16, cdStart, true);
  parts.push(eocd);

  return new Blob(parts, { type: "application/zip" });
}

const expUtf8 = s => new TextEncoder().encode(s);
const expXmlFile = (name, xml) => ({ name, data: expUtf8('<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n' + xml) });

/* ============================ Markdown ============================ */

// 单元格里的 | 会把表格列切断，换行会直接终止表格 —— 都得转义。
const expEscMd = s => expClean(s)
  .replace(/\|/g, "\\|").replace(/\r?\n/g, "<br>");

function expToMarkdown(model) {
  const L = [];
  L.push("# " + (model.title || ""), "");
  if (model.subtitle) L.push("> " + model.subtitle, "");
  if ((model.meta || []).length) {
    L.push("| 项 | 值 |", "| --- | --- |");
    model.meta.forEach(([k, v]) => L.push(`| ${expEscMd(k)} | ${expEscMd(v)} |`));
    L.push("");
  }
  if (model.narrative) {
    L.push("## " + (model.narrativeTitle || "分析结论"), "", expClean(model.narrative), "");
  }
  if ((model.figures || []).length) {
    L.push("## 图表", "");
    model.figures.forEach(f => {
      if (!f) return;
      L.push("### " + expClean(f.title || "图表"), "");
      if (f.dataUrl) L.push("![chart](" + f.dataUrl + ")", "");
      else L.push("_（图表截图不可用，请使用 HTML/PDF/Word 导出）_", "");
    });
  }
  (model.sections || []).forEach(sec => {
    if (!sec.rows || !sec.rows.length) return;
    L.push("## " + sec.title, "");
    L.push("| " + sec.columns.map(expEscMd).join(" | ") + " |");
    L.push("| " + sec.columns.map(() => "---").join(" | ") + " |");
    sec.rows.forEach(r => L.push("| " + r.map(expEscMd).join(" | ") + " |"));
    L.push("");
  });
  return L.join("\n");
}

/* ============================ Excel (.xlsx) ============================ */

// 0 → A, 25 → Z, 26 → AA
function expColRef(n) {
  let s = "";
  for (let x = n + 1; x > 0;) {
    const r = (x - 1) % 26;
    s = String.fromCharCode(65 + r) + s;
    x = (x - r - 1) / 26;
  }
  return s;
}

// Excel 工作表名硬限制：≤31 字符、不能含 []:*?/\ 、不能重复、不能为空。
// 违反其一 Excel 就报「文件已损坏」，所以这里必须兜住。
function expSheetNames(titles) {
  const used = new Set(), out = [];
  titles.forEach((t, i) => {
    // 非法字符换成空格后要合并连续空白，否则 "GPU / 加速卡" 会变成 "GPU   加速卡"
    let n = String(t || ("Sheet" + (i + 1)))
      .replace(/[[\]:*?/\\]/g, " ").replace(/\s+/g, " ").trim().slice(0, 31);
    if (!n) n = "Sheet" + (i + 1);
    let base = n, k = 2;
    while (used.has(n.toLowerCase())) {
      const suf = "(" + k++ + ")";
      n = base.slice(0, 31 - suf.length) + suf;
    }
    used.add(n.toLowerCase());
    out.push(n);
  });
  return out;
}

function expSheetXml(columns, rows) {
  const cell = (ci, ri, val, bold) => {
    const ref = expColRef(ci) + ri;
    // 一律按 inlineStr 写文本：若让 Excel 猜类型，"0012345" 这种序列号会被
    // 当成数字吃掉前导零，资产台账就对不上了。
    return `<c r="${ref}" t="inlineStr"${bold ? ' s="1"' : ""}><is><t xml:space="preserve">${expEscXml(val)}</t></is></c>`;
  };
  const widths = columns.map((c, i) => {
    let w = String(c == null ? "" : c).length;
    rows.forEach(r => { w = Math.max(w, String(r[i] == null ? "" : r[i]).length); });
    // 中文字符视觉宽度约为西文两倍，这里给个够用的近似上下限
    return Math.min(60, Math.max(8, w + 2));
  });
  const cols = `<cols>${widths.map((w, i) => `<col min="${i + 1}" max="${i + 1}" width="${w}" customWidth="1"/>`).join("")}</cols>`;
  const head = `<row r="1">${columns.map((c, i) => cell(i, 1, c, true)).join("")}</row>`;
  const body = rows.map((r, ri) =>
    `<row r="${ri + 2}">${columns.map((_, ci) => cell(ci, ri + 2, r[ci], false)).join("")}</row>`).join("");
  return `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
    `<sheetViews><sheetView workbookViewId="0">` +
    // 冻结首行：几十条内存/硬盘往下翻时表头还在
    `<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>` +
    cols + `<sheetData>${head}${body}</sheetData>` +
    `<autoFilter ref="A1:${expColRef(columns.length - 1)}${rows.length + 1}"/></worksheet>`;
}

function expToXlsx(model) {
  // 摘要单独一页，其余每个区块一页 —— 资产管理通常按部件类型筛选
  const sheets = [];
  if ((model.meta || []).length) {
    sheets.push({ title: model.summaryTitle || "摘要信息", columns: ["项", "值"], rows: model.meta.map(([k, v]) => [k, v]) });
  }
  if (model.narrative) {
    const rows = expClean(model.narrative).split(/\r?\n/).map((line, i) => [i + 1, line]);
    sheets.push({ title: model.narrativeTitle || "分析结论", columns: ["行", "内容"], rows });
  }
  (model.sections || []).forEach(sec => {
    if (sec.rows && sec.rows.length) sheets.push(sec);
  });
  if (!sheets.length) sheets.push({ title: "空", columns: ["项"], rows: [] });

  const names = expSheetNames(sheets.map(s => s.title));
  const files = [];

  files.push(expXmlFile("[Content_Types].xml",
    `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
    `<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
    `<Default Extension="xml" ContentType="application/xml"/>` +
    `<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
    sheets.map((_, i) => `<Override PartName="/xl/worksheets/sheet${i + 1}.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`).join("") +
    `<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
    `</Types>`));

  files.push(expXmlFile("_rels/.rels",
    `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
    `<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
    `</Relationships>`));

  files.push(expXmlFile("xl/workbook.xml",
    `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
    `xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>` +
    names.map((n, i) => `<sheet name="${expEscXml(n)}" sheetId="${i + 1}" r:id="rId${i + 1}"/>`).join("") +
    `</sheets></workbook>`));

  files.push(expXmlFile("xl/_rels/workbook.xml.rels",
    `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
    sheets.map((_, i) => `<Relationship Id="rId${i + 1}" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet${i + 1}.xml"/>`).join("") +
    `<Relationship Id="rId${sheets.length + 1}" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
    `</Relationships>`));

  // fills 必须至少 2 个（none + gray125），少了 Excel 会判定样式表非法
  files.push(expXmlFile("xl/styles.xml",
    `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
    `<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font>` +
    `<font><b/><sz val="11"/><name val="Calibri"/></font></fonts>` +
    `<fills count="2"><fill><patternFill patternType="none"/></fill>` +
    `<fill><patternFill patternType="gray125"/></fill></fills>` +
    `<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
    `<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
    `<cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
    `<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs>` +
    // 缺了 Normal 这个内置样式，严格的消费方（openpyxl 等）会警告"无默认样式"
    `<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
    `</styleSheet>`));

  sheets.forEach((s, i) => {
    files.push(expXmlFile(`xl/worksheets/sheet${i + 1}.xml`, expSheetXml(s.columns, s.rows || [])));
  });

  return new Blob([expZip(files)], {
    type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  });
}

/* ============================ Word (.docx) ============================ */

const expWP = (text, opts) => {
  const o = opts || {};
  const rPr = (o.bold || o.size) ? `<w:rPr>${o.bold ? "<w:b/>" : ""}${o.size ? `<w:sz w:val="${o.size}"/><w:szCs w:val="${o.size}"/>` : ""}</w:rPr>` : "";
  return `<w:p>${o.spacing ? `<w:pPr><w:spacing w:before="${o.spacing}"/></w:pPr>` : ""}` +
    `<w:r>${rPr}<w:t xml:space="preserve">${expEscXml(text)}</w:t></w:r></w:p>`;
};

function expDocxTable(columns, rows) {
  const cellXml = (t, bold) =>
    `<w:tc><w:tcPr><w:tcW w:w="0" w:type="auto"/>${bold ? '<w:shd w:val="clear" w:color="auto" w:fill="EAF1FB"/>' : ""}</w:tcPr>${expWP(t, { bold, size: 18 })}</w:tc>`;
  const borders = `<w:tblBorders>` +
    ["top", "left", "bottom", "right", "insideH", "insideV"]
      .map(s => `<w:${s} w:val="single" w:sz="4" w:space="0" w:color="BFBFBF"/>`).join("") +
    `</w:tblBorders>`;
  return `<w:tbl><w:tblPr><w:tblW w:w="5000" w:type="pct"/>${borders}</w:tblPr>` +
    `<w:tr><w:trPr><w:tblHeader/></w:trPr>${columns.map(c => cellXml(c, true)).join("")}</w:tr>` +
    rows.map(r => `<w:tr>${columns.map((_, i) => cellXml(r[i], false)).join("")}</w:tr>`).join("") +
    `</w:tbl>`;
}

function expToDocx(model) {
  let body = "";
  body += expWP(model.title || "", { bold: true, size: 32 });
  if (model.subtitle) body += expWP(model.subtitle, { size: 18 });
  if ((model.meta || []).length) {
    body += expWP(model.summaryTitle || "摘要信息", { bold: true, size: 24, spacing: 200 });
    body += expDocxTable(["项", "值"], model.meta.map(([k, v]) => [k, v]));
  }
  if (model.narrative) {
    body += expWP(model.narrativeTitle || "分析结论", { bold: true, size: 24, spacing: 200 });
    expClean(model.narrative).split(/\r?\n/).forEach(line => {
      const heading = line.match(/^\s*#{1,6}\s+(.+)$/);
      body += expWP(heading ? heading[1] : line.replace(/^\s*[-*]\s+/, "• "), {
        bold: !!heading, size: heading ? 22 : 19
      });
    });
  }
  (model.sections || []).forEach(sec => {
    if (!sec.rows || !sec.rows.length) return;
    body += expWP(sec.title, { bold: true, size: 24, spacing: 200 });
    body += expDocxTable(sec.columns, sec.rows);
  });
  if (model.footer) body += expWP(model.footer, { size: 16, spacing: 240 });
  const landscape = model.orientation === "landscape" ||
    (model.orientation !== "portrait" && (model.sections || []).some(sec => (sec.columns || []).length > 4));
  const page = landscape
    ? `<w:pgSz w:w="16838" w:h="11906" w:orient="landscape"/>`
    : `<w:pgSz w:w="11906" w:h="16838"/>`;

  const files = [
    expXmlFile("[Content_Types].xml",
      `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
      `<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
      `<Default Extension="xml" ContentType="application/xml"/>` +
      `<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
      `</Types>`),
    expXmlFile("_rels/.rels",
      `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
      `<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
      `</Relationships>`),
    expXmlFile("word/document.xml",
      `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
      `<w:body>${body}<w:sectPr>${page}` +
      `<w:pgMar w:top="720" w:right="720" w:bottom="720" w:left="720"/></w:sectPr></w:body></w:document>`),
  ];

  return new Blob([expZip(files)], {
    type: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  });
}

/* ============================ PDF（浏览器打印） ============================ */

function expPrintHTML(model) {
  // 按行内健康/状态/安全严重度文字着色（启发式；命中不到也无害）。
  const rowClass = (row) => {
    const t = (row || []).join(" ");
    if (/危急|严重|critical|crit\b|故障|预测故障|失败|failed|fault|不可用|offline|离线/i.test(t)) return ' class="sev-crit"';
    if (/高危|\bhigh\b|警告|warning|降级|degraded|注意|偏低|将坏/i.test(t)) return ' class="sev-high"';
    if (/中危|\bmedium\b|\bwarn\b/i.test(t)) return ' class="sev-warn"';
    if (/低危|\blow\b/i.test(t)) return ' class="sev-low"';
    return "";
  };
  const tbl = (columns, rows) =>
    `<table><thead><tr>${columns.map(c => `<th>${expEscXml(c)}</th>`).join("")}</tr></thead>` +
    `<tbody>${rows.map(r => `<tr${rowClass(r)}>${columns.map((_, i) => `<td>${expEscXml(r[i])}</td>`).join("")}</tr>`).join("")}</tbody></table>`;

  // 顶部 KPI 卡片：优先使用调用方显式 kpis；兼容旧硬件报告时再从 meta 挑关键指标。
  const metaMap = Object.fromEntries((model.meta || []).map(([k, v]) => [k, v]));
  const kpiPairs = (model.kpis || []).length ? model.kpis :
    ["整机健康", "异常部件", "最高温度", "总功耗", "电源冗余"]
      .filter(k => metaMap[k] != null && metaMap[k] !== "").map(k => [k, metaMap[k]]);
  const kpis = kpiPairs.map(([k, v]) =>
    `<div class="kpi"><div class="kpi-v">${expEscXml(v)}</div><div class="kpi-k">${expEscXml(k)}</div></div>`).join("");

  const narrativeHTML = (text) => {
    let inCode = false;
    return expClean(text).split(/\r?\n/).map(line => {
      if (/^\s*```/.test(line)) { inCode = !inCode; return ""; }
      if (inCode) return `<pre>${expEscXml(line)}</pre>`;
      const heading = line.match(/^\s*(#{1,6})\s+(.+)$/);
      if (heading) return `<h3>${expEscXml(heading[2])}</h3>`;
      const li = line.match(/^\s*[-*]\s+(.+)$/);
      if (li) return `<div class="n-li">• ${expEscXml(li[1])}</div>`;
      return line.trim() ? `<p>${expEscXml(line)}</p>` : "";
    }).join("");
  };

  let h = `<div class="rpt-head"><h1>${expEscXml(model.title || "")}</h1>`;
  if (model.subtitle) h += `<p class="sub">${expEscXml(model.subtitle)}</p>`;
  h += `</div>`;
  if (kpis) h += `<div class="kpis">${kpis}</div>`;
  if ((model.meta || []).length) {
    h += `<h2>${expEscXml(model.summaryTitle || "摘要信息")}</h2>` + tbl(["项", "值"], model.meta.map(([k, v]) => [k, v]));
  }
  if (model.narrative) {
    h += `<h2>${expEscXml(model.narrativeTitle || "分析结论")}</h2><div class="narrative">${narrativeHTML(model.narrative)}</div>`;
  }
  if ((model.figures || []).length) {
    h += `<h2>图表</h2><div class="figures">`;
    model.figures.forEach(f => {
      if (!f || !f.dataUrl) return;
      h += `<figure class="fig"><figcaption>${expEscXml(f.title || "图表")}</figcaption><img src="${f.dataUrl}" alt="${expEscXml(f.title || "")}"/></figure>`;
    });
    h += `</div>`;
  }
  (model.sections || []).forEach(sec => {
    if (!sec.rows || !sec.rows.length) return;
    h += `<h2>${expEscXml(sec.title)} <span class="cnt">${sec.rows.length}</span></h2>` + tbl(sec.columns, sec.rows);
  });
  if (model.footer) h += `<div class="rpt-footer">${expEscXml(model.footer)}</div>`;
  const orientation = model.orientation === "portrait" ? "portrait" :
    (model.orientation === "landscape" ? "landscape" :
      ((model.sections || []).some(sec => (sec.columns || []).length > 4) ? "landscape" : "portrait"));

  // 打印一律用浅底黑字：深色主题直接打出来既费墨又难认。print-color-adjust:exact 让底色/健康色能打出来。
  return `<!doctype html><html><head><meta charset="utf-8"><title>${expEscXml(model.title || "")}</title>
<style>
  @page { size: A4 ${orientation}; margin: 12mm; }
  body { font-family:"Microsoft YaHei","PingFang SC",-apple-system,sans-serif; color:#1a1a1a; background:#fff; margin:0;
         -webkit-print-color-adjust:exact; print-color-adjust:exact; }
  .rpt-head { border-left:5px solid #2563eb; padding:1px 0 1px 12px; margin-bottom:12px; }
  h1 { font-size:19px; margin:0 0 3px; color:#111; }
  p.sub { font-size:11px; color:#666; margin:0; }
  .kpis { display:flex; gap:10px; margin:0 0 14px; flex-wrap:wrap; }
  .kpi { flex:1; min-width:110px; border:1px solid #e2e6ea; border-radius:6px; padding:8px 12px; background:#f7f9fc; }
  .kpi-v { font-size:16px; font-weight:700; color:#111; }
  .kpi-k { font-size:10px; color:#777; margin-top:2px; }
  h2 { font-size:13px; margin:16px 0 6px; padding:4px 0 4px 8px; border-left:3px solid #2563eb;
       background:#f1f5f9; color:#1e3a5f; break-after:avoid; page-break-after:avoid; }
  h2 .cnt { font-size:10px; color:#8a94a0; font-weight:400; }
  .narrative { font-size:10.5px; line-height:1.65; color:#263449; border:1px solid #e2e8f0; border-radius:6px; padding:10px 12px; }
  .narrative p { margin:0 0 5px; white-space:pre-wrap; }
  .narrative h3 { font-size:11.5px; margin:9px 0 4px; color:#1e3a5f; }
  .narrative pre { margin:0; padding:2px 6px; background:#f1f5f9; white-space:pre-wrap; font-family:ui-monospace,monospace; }
  .figures { display:flex; flex-direction:column; gap:12px; margin:0 0 12px; }
  .fig { margin:0; border:1px solid #e2e8f0; border-radius:6px; padding:8px; background:#fafbfc; break-inside:avoid; }
  .fig figcaption { font-size:11px; font-weight:600; color:#1e3a5f; margin:0 0 6px; }
  .fig img { max-width:100%; height:auto; display:block; }
  .n-li { margin:2px 0 2px 8px; }
  table { width:100%; border-collapse:collapse; font-size:10px; margin-bottom:8px; }
  th, td { border:1px solid #d0d5db; padding:4px 6px; text-align:left; word-break:break-word; }
  th { background:#eef2f7; font-weight:600; color:#334155; }
  tbody tr:nth-child(even) { background:#fafbfc; }
  tr.sev-warn { background:#fff8e6 !important; }
  tr.sev-high { background:#fff1e6 !important; }
  tr.sev-crit { background:#fdecec !important; }
  tr.sev-low { background:#f4f7fb !important; }
  tr.sev-crit td:first-child, tr.sev-high td:first-child, tr.sev-warn td:first-child { font-weight:600; }
  thead { display:table-header-group; }
  tr { break-inside:avoid; page-break-inside:avoid; }
  .rpt-footer { margin-top:14px; padding-top:8px; border-top:1px solid #d9dee5; color:#7a8491; font-size:9px; }
</style></head><body>${h}</body></html>`;
}

function expPrintPDF(model) {
  const w = model && model._printWindow ? model._printWindow : window.open("", "_blank");
  if (!w) return false; // 被拦截 → 调用方给提示
  const html = model && model.kind === "visual" ? expVisualPrintHTML(model) : expPrintHTML(model);
  w.document.write(html);
  w.document.close();
  w.focus();
  // document.write 出来的文档 onload 时机各浏览器不一，双保险触发一次即可
  let fired = false;
  const go = () => { if (fired) return; fired = true; try { w.print(); } catch (e) {} };
  w.onload = go;
  setTimeout(go, 400);
  return true;
}

/* ============================ 看板视觉导出（PNG / 视觉 PDF） ============================ */

function expLoadImage(src) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error("image load failed"));
    img.src = src;
  });
}

function expSvgToDataURL(svgEl) {
  if (!svgEl) return "";
  const clone = svgEl.cloneNode(true);
  if (!clone.getAttribute("xmlns")) clone.setAttribute("xmlns", "http://www.w3.org/2000/svg");
  // 把 CSS 变量解析成具体色值，否则离线 SVG / foreignObject 里 var(--*) 会失效
  clone.querySelectorAll("*").forEach((node, i) => {
    const live = svgEl.querySelectorAll("*")[i];
    if (!live) return;
    const cs = getComputedStyle(live);
    ["fill", "stroke", "color"].forEach(prop => {
      const v = cs.getPropertyValue(prop);
      if (v && v !== "none") node.setAttribute(prop === "color" ? "fill" : prop, v);
    });
  });
  const xml = new XMLSerializer().serializeToString(clone);
  return "data:image/svg+xml;charset=utf-8," + encodeURIComponent(xml);
}

// 把 live DOM 的关键计算样式内联到 clone，供 foreignObject 离线渲染（零依赖、CSP 友好）。
function expInlineComputedStyles(src, dst) {
  if (!src || !dst || src.nodeType !== 1 || dst.nodeType !== 1) return;
  const cs = getComputedStyle(src);
  const props = [
    "box-sizing", "display", "position", "overflow", "overflow-x", "overflow-y",
    "flex", "flex-direction", "flex-wrap", "align-items", "justify-content", "align-content",
    "gap", "row-gap", "column-gap", "grid-template-columns", "grid-template-rows",
    "width", "height", "min-width", "min-height", "max-width", "max-height",
    "margin", "padding", "border", "border-radius", "background", "background-color",
    "color", "opacity", "font", "font-size", "font-weight", "font-family", "line-height",
    "letter-spacing", "text-align", "text-overflow", "white-space", "word-break",
    "box-shadow", "outline", "vertical-align", "object-fit", "table-layout"
  ];
  let style = "box-sizing:border-box;";
  props.forEach(p => {
    const v = cs.getPropertyValue(p);
    if (v && v !== "normal" && v !== "none" && v !== "auto" && v !== "visible" && v !== "static") {
      style += p + ":" + v + ";";
    }
  });
  // 始终带上颜色与背景，避免「normal」被跳过
  style += "color:" + (cs.color || "#111") + ";";
  if (cs.backgroundColor && cs.backgroundColor !== "rgba(0, 0, 0, 0)") {
    style += "background-color:" + cs.backgroundColor + ";";
  }
  const prev = dst.getAttribute("style") || "";
  dst.setAttribute("style", prev + ";" + style);
  const sc = src.children, dc = dst.children;
  for (let i = 0; i < sc.length && i < dc.length; i++) expInlineComputedStyles(sc[i], dc[i]);
}

// 将 DOM 节点栅格化为 PNG dataURL（画布优先替换为位图，保证图表所见即所得）。
async function expCaptureElement(el, scale) {
  if (!el) return "";
  const s = Math.max(1, Math.min(3, scale || 2));
  const rect = el.getBoundingClientRect();
  const w = Math.max(1, Math.ceil(rect.width));
  const h = Math.max(1, Math.ceil(rect.height));
  const clone = el.cloneNode(true);
  const srcCanvases = el.querySelectorAll("canvas");
  const dstCanvases = clone.querySelectorAll("canvas");
  srcCanvases.forEach((c, i) => {
    const dst = dstCanvases[i];
    if (!dst || !c.width) return;
    try {
      const img = document.createElement("img");
      img.setAttribute("src", c.toDataURL("image/png"));
      img.setAttribute("width", String(c.clientWidth || c.width));
      img.setAttribute("height", String(c.clientHeight || c.height));
      img.setAttribute("style", "display:block;width:100%;height:auto;max-height:100%;");
      dst.replaceWith(img);
    } catch (e) {}
  });
  // 单 SVG 面板：优先用序列化 SVG（比整卡 foreignObject 更稳）
  const onlySvg = el.querySelector("svg") && !el.querySelector("canvas, table, .dash-logs, .dash-bars, .dash-bars-h, .dash-states, .dash-heatmap, .dash-alerts, .dash-pie-legend");
  if (onlySvg && el.querySelectorAll("svg").length === 1 && (el.children.length <= 2)) {
    try {
      const svgURL = expSvgToDataURL(el.querySelector("svg"));
      const img = await expLoadImage(svgURL);
      const canvas = document.createElement("canvas");
      canvas.width = w * s; canvas.height = h * s;
      const ctx = canvas.getContext("2d");
      ctx.fillStyle = "#ffffff";
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
      return canvas.toDataURL("image/png");
    } catch (e) {}
  }
  expInlineComputedStyles(el, clone);
  clone.setAttribute("xmlns", "http://www.w3.org/1999/xhtml");
  clone.style.cssText += `;width:${w}px;height:${h}px;overflow:hidden;background:#fff;`;
  // 去掉编辑态控件（若克隆里仍在）
  clone.querySelectorAll(".panel-edit-actions, .dash-resize, .dash-panel-coord, .dash-drag-handle").forEach(n => n.remove());
  let xhtml;
  try { xhtml = new XMLSerializer().serializeToString(clone); }
  catch (e) { return ""; }
  if (!/^<\w+:/.test(xhtml) && !xhtml.includes("xmlns=")) {
    xhtml = xhtml.replace(/^\s*<([a-zA-Z0-9-]+)/, '<$1 xmlns="http://www.w3.org/1999/xhtml"');
  }
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}"><foreignObject width="100%" height="100%">${xhtml}</foreignObject></svg>`;
  const url = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg);
  try {
    const img = await expLoadImage(url);
    const canvas = document.createElement("canvas");
    canvas.width = w * s; canvas.height = h * s;
    const ctx = canvas.getContext("2d");
    ctx.fillStyle = "#ffffff";
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
    return canvas.toDataURL("image/png");
  } catch (e) {
    // foreignObject 失败时：至少导出画布/SVG
    const c = el.querySelector("canvas");
    if (c && c.width) try { return c.toDataURL("image/png"); } catch (_) {}
    const svgEl = el.querySelector("svg");
    if (svgEl) return expSvgToDataURL(svgEl);
    return "";
  }
}

function expDataURLToUint8(dataUrl) {
  const m = String(dataUrl || "").match(/^data:([^;]+);base64,(.+)$/);
  if (!m) return null;
  const bin = atob(m[2]);
  const u8 = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i);
  return { mime: m[1], data: u8, ext: /png/i.test(m[1]) ? "png" : (/jpeg|jpg/i.test(m[1]) ? "jpg" : "bin") };
}

// 视觉看板打印页：按 24 栏网格排布各面板截图，打印即「看板成品」而非数据表。
function expVisualPrintHTML(model) {
  const cols = model.cols || 24;
  const panels = model.visualPanels || [];
  const maxY = panels.reduce((m, p) => Math.max(m, (p.y || 0) + (p.h || 1)), 1);
  const cells = panels.map(p => {
    const x = (p.x || 0) + 1, y = (p.y || 0) + 1, w = p.w || 12, h = p.h || 8;
    const img = p.dataUrl
      ? `<img src="${p.dataUrl}" alt="${expEscXml(p.title || "")}"/>`
      : `<div class="empty">${expEscXml(p.empty || "无内容")}</div>`;
    return `<div class="cell" style="grid-column:${x} / span ${w};grid-row:${y} / span ${h}">${img}</div>`;
  }).join("");
  const meta = (model.meta || []).map(([k, v]) =>
    `<span><b>${expEscXml(k)}</b>${expEscXml(v)}</span>`).join("");
  return `<!doctype html><html><head><meta charset="utf-8"><title>${expEscXml(model.title || "")}</title>
<style>
  @page { size: A4 landscape; margin: 8mm; }
  * { box-sizing: border-box; }
  body { font-family:"Microsoft YaHei","PingFang SC",-apple-system,sans-serif; color:#111; background:#fff; margin:0;
         -webkit-print-color-adjust:exact; print-color-adjust:exact; }
  .head { display:flex; justify-content:space-between; align-items:flex-end; gap:12px; margin-bottom:8px;
          border-bottom:2px solid #2563eb; padding-bottom:6px; }
  h1 { font-size:16px; margin:0; }
  .sub { font-size:10px; color:#666; margin:2px 0 0; }
  .meta { display:flex; flex-wrap:wrap; gap:8px 14px; font-size:9px; color:#555; margin-bottom:8px; }
  .meta b { color:#334155; margin-right:4px; }
  .board { display:grid; grid-template-columns:repeat(${cols},1fr); grid-auto-rows:minmax(28px, auto);
           gap:5px; width:100%; min-height:${Math.max(400, maxY * 32)}px; }
  .cell { border:1px solid #d8dee6; border-radius:6px; overflow:hidden; background:#fff;
          break-inside:avoid; page-break-inside:avoid; min-height:0; display:flex; }
  .cell img { width:100%; height:100%; object-fit:fill; display:block; }
  .cell .empty { padding:12px; font-size:11px; color:#888; }
  .foot { margin-top:8px; font-size:9px; color:#8a94a0; border-top:1px solid #e5e7eb; padding-top:4px; }
</style></head><body>
  <div class="head"><div><h1>${expEscXml(model.title || "")}</h1>
  ${model.subtitle ? `<p class="sub">${expEscXml(model.subtitle)}</p>` : ""}</div></div>
  ${meta ? `<div class="meta">${meta}</div>` : ""}
  <div class="board">${cells}</div>
  ${model.footer ? `<div class="foot">${expEscXml(model.footer)}</div>` : ""}
</body></html>`;
}

// 将 visualPanels 拼成一张整板 PNG Blob。
async function expComposeBoardPNG(model) {
  const panels = model.visualPanels || [];
  const cols = model.cols || 24;
  const gap = model.gap != null ? model.gap : 6;
  const colW = model.colW || 48;
  const rowH = model.rowH || 28;
  const scale = model.scale || 2;
  let maxY = 1;
  panels.forEach(p => { maxY = Math.max(maxY, (p.y || 0) + (p.h || 1)); });
  const boardW = cols * colW + (cols - 1) * gap;
  const boardH = maxY * rowH + Math.max(0, maxY - 1) * gap;
  const headH = 52;
  const canvas = document.createElement("canvas");
  canvas.width = Math.ceil(boardW * scale);
  canvas.height = Math.ceil((boardH + headH + 16) * scale);
  const ctx = canvas.getContext("2d");
  ctx.scale(scale, scale);
  ctx.fillStyle = "#f1f5f9";
  ctx.fillRect(0, 0, boardW, boardH + headH + 16);
  ctx.fillStyle = "#ffffff";
  ctx.fillRect(0, 0, boardW, headH);
  ctx.fillStyle = "#2563eb";
  ctx.fillRect(0, headH - 2, boardW, 2);
  ctx.fillStyle = "#111827";
  ctx.font = "600 15px 'PingFang SC','Microsoft YaHei',sans-serif";
  ctx.fillText(String(model.title || "Dashboard").slice(0, 80), 12, 22);
  ctx.fillStyle = "#64748b";
  ctx.font = "11px 'PingFang SC','Microsoft YaHei',sans-serif";
  ctx.fillText(String(model.subtitle || "").slice(0, 120), 12, 40);

  for (const p of panels) {
    const x = (p.x || 0) * (colW + gap);
    const y = headH + 8 + (p.y || 0) * (rowH + gap);
    const w = (p.w || 1) * colW + ((p.w || 1) - 1) * gap;
    const h = (p.h || 1) * rowH + ((p.h || 1) - 1) * gap;
    ctx.fillStyle = "#ffffff";
    ctx.strokeStyle = "#d8dee6";
    ctx.lineWidth = 1;
    ctx.beginPath();
    if (ctx.roundRect) ctx.roundRect(x, y, w, h, 6); else ctx.rect(x, y, w, h);
    ctx.fill(); ctx.stroke();
    if (p.dataUrl) {
      try {
        const img = await expLoadImage(p.dataUrl);
        ctx.drawImage(img, x + 1, y + 1, Math.max(1, w - 2), Math.max(1, h - 2));
      } catch (e) {
        ctx.fillStyle = "#94a3b8";
        ctx.font = "12px sans-serif";
        ctx.fillText(p.title || "panel", x + 8, y + 20);
      }
    } else {
      ctx.fillStyle = "#94a3b8";
      ctx.font = "12px sans-serif";
      ctx.fillText((p.title || "") + " · " + (p.empty || "无内容"), x + 8, y + 20);
    }
  }
  return new Promise((resolve, reject) => {
    canvas.toBlob(b => b ? resolve(b) : reject(new Error("png encode failed")), "image/png");
  });
}

// 视觉 Word：封面元信息 + 每面板一张截图（成品而非数据表）。
function expToDocxVisual(model) {
  const panels = (model.visualPanels || []).filter(p => p.dataUrl);
  const media = [];
  let body = "";
  body += expWP(model.title || "", { bold: true, size: 32 });
  if (model.subtitle) body += expWP(model.subtitle, { size: 18 });
  if ((model.meta || []).length) {
    body += expWP(model.summaryTitle || "看板信息", { bold: true, size: 24, spacing: 200 });
    body += expDocxTable(["项", "值"], model.meta.map(([k, v]) => [k, v]));
  }
  const rels = [];
  panels.forEach((p, i) => {
    const bin = expDataURLToUint8(p.dataUrl);
    if (!bin) return;
    const name = `image${i + 1}.${bin.ext}`;
    media.push({ name: "word/media/" + name, data: bin.data });
    const rid = "rIdImg" + (i + 1);
    rels.push({ id: rid, target: "media/" + name });
    const cx = Math.round(Math.min(16.5, Math.max(6, (p.w || 12) / 24 * 16.5)) * 914400); // EMUs
    const cy = Math.round(Math.min(10, Math.max(2.2, (p.h || 8) / 24 * 9)) * 914400);
    body += expWP(p.title || ("面板 " + (i + 1)), { bold: true, size: 22, spacing: 200 });
    body += `<w:p><w:r><w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"
      xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"
      xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
      xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"
      xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
      <wp:extent cx="${cx}" cy="${cy}"/><wp:docPr id="${i + 1}" name="${expEscXml(name)}"/>
      <a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture">
      <pic:pic><pic:nvPicPr><pic:cNvPr id="${i + 1}" name="${expEscXml(name)}"/><pic:cNvPicPr/></pic:nvPicPr>
      <pic:blipFill><a:blip r:embed="${rid}"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill>
      <pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="${cx}" cy="${cy}"/></a:xfrm>
      <a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic>
      </a:graphicData></a:graphic></wp:inline></w:drawing></w:r></w:p>`;
  });
  if (model.footer) body += expWP(model.footer, { size: 16, spacing: 240 });
  const page = `<w:pgSz w:w="16838" w:h="11906" w:orient="landscape"/>`;
  const files = [
    expXmlFile("[Content_Types].xml",
      `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
      `<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
      `<Default Extension="xml" ContentType="application/xml"/>` +
      `<Default Extension="png" ContentType="image/png"/>` +
      `<Default Extension="jpg" ContentType="image/jpeg"/>` +
      `<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
      `</Types>`),
    expXmlFile("_rels/.rels",
      `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
      `<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
      `</Relationships>`),
    expXmlFile("word/_rels/document.xml.rels",
      `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
      rels.map(r => `<Relationship Id="${r.id}" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="${r.target}"/>`).join("") +
      `</Relationships>`),
    expXmlFile("word/document.xml",
      `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" ` +
      `xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
      `xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" ` +
      `xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
      `xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture">` +
      `<w:body>${body}<w:sectPr>${page}` +
      `<w:pgMar w:top="720" w:right="720" w:bottom="720" w:left="720"/></w:sectPr></w:body></w:document>`),
    ...media,
  ];
  return new Blob([expZip(files)], {
    type: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  });
}

/* ============================ 统一入口 ============================ */

// 独立 HTML 报告（可离线打开 / 浏览器另存为 PDF）
function expToHTML(model) {
  // 复用打印样式，并加屏幕阅读友好的外边距与操作提示。
  const inner = expPrintHTML(model);
  return inner.replace(
    "</head>",
    `<style>
  @media screen {
    body { max-width: 960px; margin: 24px auto; padding: 0 20px 40px; }
    .rpt-screen-tip {
      margin: 0 0 14px; padding: 8px 12px; border-radius: 6px;
      background: #eff6ff; border: 1px solid #bfdbfe; color: #1e40af; font-size: 12px;
    }
  }
</style></head>`
  ).replace(
    "<body>",
    `<body><div class="rpt-screen-tip">提示：可用浏览器「打印 → 另存为 PDF」生成正式 PDF 交付件。</div>`
  );
}

function expToJSON(model) {
  if (model && model.rawJSON != null) return JSON.stringify(model.rawJSON, null, 2);
  return JSON.stringify({
    title: model && model.title,
    subtitle: model && model.subtitle,
    generated_at: new Date().toISOString(),
    meta: (model && model.meta) || [],
    kpis: (model && model.kpis) || [],
    narrative: model && model.narrative,
    sections: (model && model.sections) || [],
    footer: model && model.footer,
  }, null, 2);
}

// fmt: markdown | excel | word | html | pdf | pdf-report | png | json
// model.kind === "visual" 时：pdf/word/png 走看板成品路径；excel 仍按 sections 数据表。
async function exportModel(model, fmt, baseName) {
  const name = expSafeName(baseName || model.title) + "_" + expStamp();
  const visual = model && model.kind === "visual";
  switch (fmt) {
    case "markdown":
      expDownload(new Blob([expToMarkdown(model)], { type: "text/markdown;charset=utf-8" }), name + ".md");
      return true;
    case "excel":
      expDownload(expToXlsx(model), name + ".xlsx");
      return true;
    case "html":
      expDownload(new Blob([expToHTML(model)], { type: "text/html;charset=utf-8" }), name + ".html");
      return true;
    case "json":
      expDownload(new Blob([expToJSON(model)], { type: "application/json;charset=utf-8" }), name + ".json");
      return true;
    case "word":
      if (visual) {
        if (!(model.visualPanels || []).some(p => p.dataUrl)) {
          throw new Error("未能截取到面板画面，请待图表加载完成后重试");
        }
        expDownload(expToDocxVisual(model), name + ".docx");
      } else {
        expDownload(expToDocx(model), name + ".docx");
      }
      return true;
    case "png": {
      if (!visual) return false;
      if (!(model.visualPanels || []).some(p => p.dataUrl)) {
        throw new Error("未能截取到面板画面，请待图表加载完成后重试");
      }
      const blob = await expComposeBoardPNG(model);
      expDownload(blob, name + ".png");
      return true;
    }
    case "pdf":
      if (visual && !(model.visualPanels || []).some(p => p.dataUrl)) {
        throw new Error("未能截取到面板画面，请待图表加载完成后重试");
      }
      return expPrintPDF(model);
    case "pdf-report":
      return expPrintPDF(Object.assign({}, model, { kind: undefined, _printWindow: model._printWindow }));
    default:
      return false;
  }
}
