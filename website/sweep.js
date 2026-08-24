const { execFileSync } = require('child_process');
const pages = ['index','features','solutions','comparison','faq','contact','pricing','cases'];
const langs = ['zh-CN','zh-TW','en','ja','ko'];
const node = 'C:\\Users\\Eason\\.workbuddy\\binaries\\node\\versions\\22.22.2\\node.exe';
const NP = 'C:\\Users\\Eason\\.workbuddy\\binaries\\node\\workspace\\node_modules';
const env = Object.assign({}, process.env, { NODE_PATH: NP });
let bad = 0; let checked = 0;
for (const p of pages) for (const l of langs) {
  let out;
  try {
    out = execFileSync(node, ['verify_site.js', p + '.html', l], { encoding: 'utf8', env: env });
  } catch (e) {
    console.log('SPAWN_ERR', p, l, String(e.stdout || '').slice(0,200), '|', String(e.stderr || '').slice(0,200), '|', String(e.message || '').slice(0,200));
    bad++; checked++; continue;
  }
  let r;
  try { r = JSON.parse(out); } catch (e) { console.log('PARSE_ERR', p, l, out.slice(0,300)); bad++; checked++; continue; }
  checked++;
  if (r.issues && r.issues.length) { console.log('ISSUE', p, l, r.issues.join('|')); bad++; }
  else console.log('OK', p, l);
}
console.log(bad === 0 ? ('ALL_' + checked + '_COMBOS_OK') : ('TOTAL_ISSUES=' + bad));
