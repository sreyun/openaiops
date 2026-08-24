/* ---------- SQL 工具（MySQL / PostgreSQL 美化·审核·优化·EXPLAIN·慢SQL）---------- */
let SQL_CONNS = [];
let SQL_CHANGES = [];
let SQL_HISTORY = [];
let SQL_SCHEMA = { databases: [], database: "", tables: [], table: "", columns: [] };
let SQL_SLOW_REPORT = null;
let SQL_SLOW_Q = "";
let SQL_SLOW_TYPE = "";
let SQL_SLOW_SORT = "avg_desc";
let SQL_SLOW_PAGE = 1;
let SQL_SLOW_SIZE = 20;
let SQL_SLOW_VIEW = []; // filtered+sorted {it, idx} for current report
let SQL_VERIFY_SQL = "";
let SQL_LAST = { audit: null, optimize: null, explain: null, beautified: "", query: null };

function sqlT(k, fb) {
  return (typeof I18N !== "undefined" && I18N.t) ? (I18N.t(k, fb) || fb) : fb;
}

async function loadSQLToolkit() {
  await Promise.all([loadSQLConnections(), loadSQLChangeRequests(), loadSQLHistory()]);
  renderSQLConnSelect();
  renderSQLHistory();
  const conn = $("sqlConnSel") && $("sqlConnSel").value;
  if (conn) {
    const c = sqlConnById(conn);
    await loadSQLSchema(conn, "", (c && c.database) || "");
  } else {
    renderSQLDbSelectEmpty();
  }
  const tab = document.querySelector("#sqlInnerTabs .tab.active");
  const name = tab && tab.dataset.sqlTab ? tab.dataset.sqlTab : "workbench";
  showSQLTab(name);
}

async function loadSQLConnections() {
  try {
    const j = await fetch(`${API}/sql/connections`).then(r => r.json());
    SQL_CONNS = Array.isArray(j.connections) ? j.connections : (Array.isArray(j) ? j : []);
  } catch (e) {
    SQL_CONNS = [];
  }
}

function renderSQLConnSelect() {
  const sel = $("sqlConnSel");
  if (!sel) return;
  const prev = sel.value;
  const enabled = SQL_CONNS.filter(c => c.enabled !== false);
  sel.innerHTML = `<option value="">${esc(sqlT("sql.no_conn", "不连库（仅离线）"))}</option>` +
    enabled.map(c => {
      const drv = c.driver === "postgres" ? "pg" : "mysql";
      const port = c.port || (c.driver === "postgres" ? 5432 : 3306);
      return `<option value="${esc(c.id)}">[${esc(c.env || "prod")}/${drv}] ${esc(c.name)} (${esc(c.host)}:${port})</option>`;
    }).join("");
  if (prev && enabled.some(c => String(c.id) === String(prev))) sel.value = prev;
  if (!sel.dataset.sqlConnBound) {
    sel.dataset.sqlConnBound = "1";
    sel.addEventListener("change", () => {
      const id = sel.value;
      syncSQLDialectFromConn(id);
      syncSQLWorkbenchForDriver(id);
      if (id) {
        const c = sqlConnById(id);
        const prefer = (c && c.database) || "";
        loadSQLSchema(id, "", prefer || undefined).then(() => syncSQLDbSelect(id));
      } else {
        renderSQLSchemaEmpty();
        renderSQLDbSelectEmpty();
      }
    });
  }
  syncSQLDialectFromConn(sel.value);
  syncSQLWorkbenchForDriver(sel.value);
}

function sqlConnById(id) {
  const key = String(id || "");
  if (!key) return null;
  return SQL_CONNS.find(c => String(c.id) === key) || null;
}

function sqlActiveSchema() {
  const sel = $("sqlDbSel");
  if (sel && sel.value) return String(sel.value).trim();
  return (SQL_SCHEMA && SQL_SCHEMA.database) || "";
}

function renderSQLDbSelectEmpty(placeholder) {
  const sel = $("sqlDbSel");
  if (!sel) return;
  sel.innerHTML = `<option value="">${esc(placeholder || sqlT("sql.db_pick", "选择数据库 / Schema"))}</option>`;
  sel.disabled = true;
}

function syncSQLDbSelect(connId, prefer) {
  const sel = $("sqlDbSel");
  if (!sel) return;
  const dbs = (SQL_SCHEMA && Array.isArray(SQL_SCHEMA.databases)) ? SQL_SCHEMA.databases.slice() : [];
  const conn = sqlConnById(connId);
  const isPg = !!(conn && String(conn.driver || "") === "postgres");
  const connDb = (conn && conn.database) || "";
  // 候选只能来自服务端真的列出来的那批。
  //
  // 这里原来会把「连接配置里的库名」unshift 进列表再选中它。对 PostgreSQL 是错的：
  // 这个下拉框列的是 **schema**（服务端查的是 pg_namespace），而连接配置里的
  // database 是**库名**。于是每个 PG 连接一打开就默认选中一个不存在的 schema，
  // 运行时 search_path 被设成它，任何一句 SELECT 都报 relation ... does not exist。
  // 同一条规则在新版控制台是 frontend/src/shared/sql-schema.ts（有单测钉着）。
  const wanted = prefer != null ? String(prefer).trim() : "";
  let cur = [wanted, SQL_SCHEMA.database || "", connDb].filter(Boolean).find(x => dbs.indexOf(x) >= 0) || "";
  if (!cur && isPg && dbs.indexOf("public") >= 0) cur = "public";
  if (!cur && dbs.length === 1) cur = dbs[0];
  if (!dbs.length) {
    // 服务端一个都没列出来（多半是权限不足）。MySQL 下库=schema，用连接自带的库名
    // 兜底仍然是对的，EXPLAIN 也还能用；PostgreSQL 下拿库名当 schema 只会把
    // search_path 设错，宁可留空让人自己选。
    const fallbackDb = isPg ? "" : (wanted || connDb);
    if (fallbackDb) {
      sel.innerHTML = `<option value="${esc(fallbackDb)}">${esc(fallbackDb)}</option>`;
      sel.value = fallbackDb;
      sel.disabled = false;
      SQL_SCHEMA.database = fallbackDb;
    } else {
      SQL_SCHEMA.database = "";
      renderSQLDbSelectEmpty(sqlT("sql.db_none", "暂无库（选择连接后自动加载）"));
    }
    return;
  }
  sel.disabled = false;
  sel.innerHTML = `<option value="">${esc(sqlT("sql.db_pick", "选择数据库 / Schema"))}</option>` +
    dbs.map(n => `<option value="${esc(n)}">${esc(n)}</option>`).join("");
  sel.value = cur;
  // cur 为空 = 有多个候选且没有任何线索。这时必须把上一次的选择也一并清掉，
  // 否则界面显示"请选择"、实际却仍拿着上一个连接的库名去跑查询。
  SQL_SCHEMA.database = cur;
  if (!sel.dataset.sqlDbBound) {
    sel.dataset.sqlDbBound = "1";
    sel.addEventListener("change", () => {
      const id = ($("sqlConnSel") && $("sqlConnSel").value) || "";
      const db = sel.value || "";
      SQL_SCHEMA.database = db;
      if (id && db) loadSQLSchema(id, "", db);
      else if (id) loadSQLSchema(id, "", "");
    });
  }
}

function setActiveSQLDatabase(db, opts) {
  opts = opts || {};
  const name = String(db || "").trim();
  SQL_SCHEMA.database = name;
  const sel = $("sqlDbSel");
  if (sel) {
    if (name && ![...sel.options].some(o => o.value === name)) {
      const opt = document.createElement("option");
      opt.value = name;
      opt.textContent = name + (opts.inferred ? "（推断）" : "");
      sel.appendChild(opt);
    }
    sel.value = name;
    sel.disabled = false;
  }
  const tag = $("sqlSchemaDb");
  if (tag && name && !tag.querySelector("button")) tag.textContent = name;
}

function syncSQLDialectFromConn(id) {
  const c = sqlConnById(id);
  const el = $("sqlDialect");
  if (!el || !c) return;
  if (c.driver === "postgres") el.value = "postgres";
  else if (c.version_hint === "mysql57") el.value = "mysql57";
  else el.value = "mysql80";
}

function syncSQLWorkbenchForDriver(id) {
  const c = sqlConnById(id);
  const isPG = c && c.driver === "postgres";
  const ddlBtn = $("sqlSubmitChangeBtn");
  if (ddlBtn) {
    ddlBtn.style.display = isPG ? "none" : "";
    ddlBtn.title = isPG ? "PostgreSQL 不支持 DDL 变更单" : "";
  }
}

function renderSQLSchemaEmpty() {
  const db = $("sqlSchemaDb"); if (db) db.textContent = "";
  const tb = $("sqlSchemaTables"); if (tb) tb.innerHTML = `<span class="hint">${esc(sqlT("sql.schema_pick_conn", "选择连接后浏览表"))}</span>`;
  const col = $("sqlSchemaColumns"); if (col) col.innerHTML = "";
  SQL_SCHEMA = { databases: [], database: "", tables: [], table: "", columns: [] };
  renderSQLDbSelectEmpty();
}

async function loadSQLSchema(connId, table, database) {
  if (!connId) { renderSQLSchemaEmpty(); return; }
  try {
    // 列详情
    if (table) {
      const params = new URLSearchParams();
      const dbName = (database != null && database !== "") ? database : (SQL_SCHEMA.database || sqlActiveSchema() || "");
      if (dbName) params.set("database", dbName);
      params.set("table", table);
      const j = await fetch(`${API}/sql/connections/${encodeURIComponent(connId)}/schema?${params}`).then(r => r.json());
      if (j && j.error) throw new Error(j.error);
      SQL_SCHEMA.table = table;
      SQL_SCHEMA.database = j.database || dbName || "";
      SQL_SCHEMA.columns = Array.isArray(j.columns) ? j.columns : [];
      setActiveSQLDatabase(SQL_SCHEMA.database);
      renderSQLSchemaColumns(j);
      return;
    }

    // 先拉库列表（用于顶部 Database 下拉）
    const listRes = await fetch(`${API}/sql/connections/${encodeURIComponent(connId)}/schema`).then(r => r.json());
    if (listRes && listRes.error) throw new Error(listRes.error);
    if (Array.isArray(listRes.databases)) {
      SQL_SCHEMA.databases = listRes.databases;
    } else if (listRes.database) {
      // 连接已绑定默认库，接口直接回表列表
      SQL_SCHEMA.databases = [listRes.database];
      SQL_SCHEMA.tables = Array.isArray(listRes.tables) ? listRes.tables : [];
      SQL_SCHEMA.database = listRes.database;
      SQL_SCHEMA.table = "";
      SQL_SCHEMA.columns = [];
      setActiveSQLDatabase(listRes.database);
      syncSQLDbSelect(connId, listRes.database);
      renderSQLSchemaTables(connId, listRes);
      return;
    }

    const c = sqlConnById(connId);
    let dbName = (database != null && String(database).trim()) ? String(database).trim()
      : (SQL_SCHEMA.database || (c && c.database) || "");
    if (!dbName && SQL_SCHEMA.databases.length === 1) {
      dbName = SQL_SCHEMA.databases[0];
    }
    syncSQLDbSelect(connId, dbName);

    if (!dbName) {
      SQL_SCHEMA.database = "";
      SQL_SCHEMA.tables = [];
      SQL_SCHEMA.table = "";
      SQL_SCHEMA.columns = [];
      renderSQLSchemaDatabases(connId, SQL_SCHEMA.databases);
      return;
    }

    const params = new URLSearchParams({ database: dbName });
    const j = await fetch(`${API}/sql/connections/${encodeURIComponent(connId)}/schema?${params}`).then(r => r.json());
    if (j && j.error) throw new Error(j.error);
    SQL_SCHEMA.tables = Array.isArray(j.tables) ? j.tables : [];
    SQL_SCHEMA.database = j.database || dbName;
    SQL_SCHEMA.table = "";
    SQL_SCHEMA.columns = [];
    setActiveSQLDatabase(SQL_SCHEMA.database);
    renderSQLSchemaTables(connId, j);
  } catch (e) {
    renderSQLSchemaEmpty();
    if (typeof toast === "function") toast(String(e.message || e), "err");
  }
}

function renderSQLSchemaDatabases(connId, databases) {
  const db = $("sqlSchemaDb");
  if (db) db.textContent = sqlT("sql.schema_all_dbs", "全部库（请在上方选择）");
  const box = $("sqlSchemaTables");
  if (!box) return;
  const dbs = Array.isArray(databases) ? databases : [];
  if (!dbs.length) {
    box.innerHTML = `<span class="hint">${esc(sqlT("sql.schema_empty_db", "无业务库或无权查看"))}</span>`;
    const col = $("sqlSchemaColumns"); if (col) col.innerHTML = "";
    return;
  }
  box.innerHTML = dbs.map(name =>
    `<button type="button" class="${SQL_SCHEMA.database === name ? "active" : ""}" data-sqlschema-db="${esc(name)}">${esc(name)}</button>`
  ).join("");
  box.querySelectorAll("[data-sqlschema-db]").forEach(btn => {
    btn.onclick = () => {
      const name = btn.dataset.sqlschemaDb;
      setActiveSQLDatabase(name);
      loadSQLSchema(connId, "", name);
    };
  });
  const col = $("sqlSchemaColumns"); if (col) col.innerHTML = `<span class="hint">${esc(sqlT("sql.schema_pick_db", "请在上方 Database 下拉框选择库，或点击左侧库名"))}</span>`;
}

function renderSQLSchemaTables(connId, j) {
  const db = $("sqlSchemaDb");
  if (db) {
    const name = j.database || SQL_SCHEMA.database || "";
    db.textContent = name || "";
    if (name && SQL_SCHEMA.databases && SQL_SCHEMA.databases.length) {
      db.innerHTML = `<button type="button" class="btn ghost sm" id="sqlSchemaBackDb" title="返回库列表">← ${esc(name)}</button>`;
      const back = $("sqlSchemaBackDb");
      if (back) back.onclick = () => loadSQLSchema(connId, "", "");
    }
  }
  const box = $("sqlSchemaTables");
  if (!box) return;
  const tables = Array.isArray(j.tables) ? j.tables : [];
  if (!tables.length) {
    box.innerHTML = `<span class="hint">${esc(sqlT("sql.schema_empty", "无表或无权查看"))}</span>`;
    const col = $("sqlSchemaColumns"); if (col) col.innerHTML = "";
    return;
  }
  box.innerHTML = tables.map(t =>
    `<button type="button" class="${SQL_SCHEMA.table === t ? "active" : ""}" data-sqlschema-table="${esc(t)}">${esc(t)}</button>`
  ).join("");
  box.querySelectorAll("[data-sqlschema-table]").forEach(btn => {
    btn.onclick = () => loadSQLSchema(connId, btn.dataset.sqlschemaTable, SQL_SCHEMA.database || j.database || "");
  });
}

function renderSQLSchemaColumns(j) {
  const tb = $("sqlSchemaTables");
  if (tb) tb.querySelectorAll("[data-sqlschema-table]").forEach(b => {
    b.classList.toggle("active", b.dataset.sqlschemaTable === j.table);
  });
  const box = $("sqlSchemaColumns");
  if (!box) return;
  const cols = Array.isArray(j.columns) ? j.columns : [];
  if (!cols.length) { box.innerHTML = ""; return; }
  box.innerHTML = `<div class="hint">${esc(j.table || "")} · ${cols.length} cols</div>` +
    cols.map(c => `<div class="mono">${esc(c.Field || c.field || "?")} <span class="tag">${esc(c.Type || c.type || "")}</span></div>`).join("");
}

async function loadSQLHistory() {
  try {
    const j = await fetch(`${API}/sql/history`).then(r => r.json());
    SQL_HISTORY = Array.isArray(j.history) ? j.history : [];
  } catch (_) { SQL_HISTORY = []; }
}

function renderSQLHistory() {
  const box = $("sqlHistoryList");
  if (!box) return;
  if (!SQL_HISTORY.length) {
    box.innerHTML = `<span class="hint">${esc(sqlT("sql.history_empty", "暂无历史"))}</span>`;
    return;
  }
  box.innerHTML = SQL_HISTORY.slice(0, 20).map(h => {
    const when = h.created_at ? new Date(h.created_at * 1000).toLocaleString() : "";
    const score = h.score != null ? ` · ${h.score}` : "";
    return `<button type="button" class="sql-history-item" data-sqlhist="${esc(h.id)}">
      <span class="tag">${esc(h.kind || "query")}${score}</span>
      <div class="mono">${esc(h.sql || "")}</div>
      <div class="hint">${esc(when)}</div>
    </button>`;
  }).join("");
  box.querySelectorAll("[data-sqlhist]").forEach(btn => {
    btn.onclick = () => {
      const item = SQL_HISTORY.find(x => x.id === btn.dataset.sqlhist);
      if (!item) return;
      if (item.connection_id && $("sqlConnSel")) $("sqlConnSel").value = item.connection_id;
      setSQLText(item.sql || "");
      toast(sqlT("sql.history_reopened", "已重新打开"), "ok");
    };
  });
}

function pickVerifySQL() {
  const stored = (SQL_VERIFY_SQL || "").trim();
  if (stored && !/^\s*(create|alter|drop)\b/i.test(stored)) return stored;
  const cur = sqlText().trim();
  if (cur && !/^\s*(create|alter|drop)\b/i.test(cur)) return cur;
  return "";
}
window.pickVerifySQL = pickVerifySQL;

function showSQLTab(name) {
  document.querySelectorAll("#sqlInnerTabs .tab").forEach(t => t.classList.toggle("active", t.dataset.sqlTab === name));
  const wb = $("sqlWorkbench");
  const cm = $("sqlConnManage");
  const ch = $("sqlChangeManage");
  const ss = $("sqlSlowManage");
  const pr = $("sqlProcessManage");
  if (wb) wb.style.display = name === "workbench" ? "" : "none";
  if (cm) cm.style.display = name === "connections" ? "" : "none";
  if (ch) ch.style.display = name === "changes" ? "" : "none";
  if (ss) ss.style.display = name === "slowsql" ? "" : "none";
  if (pr) pr.style.display = name === "process" ? "" : "none";
  if (name === "connections") renderSQLConnList();
  if (name === "changes") loadSQLChangeRequests().then(renderSQLChangeList);
  if (name === "slowsql") {
    renderSQLSlowConnSelect();
    loadSQLSlowLatest();
  }
  if (name === "process") {
    renderSQLProcessConnSelect();
    loadSQLProcessLocks();
  }
}

function renderSQLProcessConnSelect() {
  const sel = $("sqlProcessConnSel");
  if (!sel) return;
  const prev = sel.value || ($("sqlConnSel") && $("sqlConnSel").value) || "";
  const enabled = SQL_CONNS.filter(c => c.enabled !== false);
  sel.innerHTML = enabled.map(c =>
    `<option value="${esc(c.id)}">[${esc(c.driver || "mysql")}/${esc(c.env || "prod")}] ${esc(c.name)}</option>`
  ).join("") || `<option value="">${esc(sqlT("sql.no_conn_enabled", "无可用连接"))}</option>`;
  if (prev && enabled.some(c => c.id === prev)) sel.value = prev;
  if (!sel.dataset.bound) {
    sel.dataset.bound = "1";
    sel.addEventListener("change", () => loadSQLProcessLocks());
  }
}

async function loadSQLProcessLocks() {
  const sel = $("sqlProcessConnSel");
  const id = sel && sel.value;
  const panel = $("sqlProcessPanel");
  if (!panel) return;
  if (!id) {
    panel.innerHTML = `<div class="hint">请先选择连接</div>`;
    return;
  }
  panel.innerHTML = `<div class="hint">${esc(sqlT("ui.loading", "加载中…"))}</div>`;
  try {
    const [pRes, lRes] = await Promise.all([
      fetch(`${API}/sql/connections/${encodeURIComponent(id)}/processlist`).then(async r => ({ ok: r.ok, j: await r.json().catch(() => ({})) })),
      fetch(`${API}/sql/connections/${encodeURIComponent(id)}/locks`).then(async r => ({ ok: r.ok, j: await r.json().catch(() => ({})) })),
    ]);
    if (!pRes.ok) throw new Error(pRes.j.error || "processlist failed");
    const procs = pRes.j.processes || [];
    const locks = (lRes.ok && lRes.j.locks) || [];
    let html = `<div class="section-title"><span>PROCESSLIST</span><span class="tag">${procs.length}</span></div>`;
    if (!procs.length) html += `<div class="hint">无会话</div>`;
    else {
      html += `<div class="nf-table-wrap"><table class="data-table"><thead><tr>
        <th>ID</th><th>User</th><th>Host</th><th>DB</th><th>Cmd</th><th>Time</th><th>Info</th><th></th>
      </tr></thead><tbody>`;
      procs.slice(0, 100).forEach(p => {
        html += `<tr>
          <td class="mono">${p.id}</td><td>${esc(p.user || "")}</td><td class="mono">${esc(p.host || "")}</td>
          <td>${esc(p.db || "")}</td><td>${esc(p.command || "")}</td><td>${p.time_sec || 0}</td>
          <td class="mono" style="max-width:280px;word-break:break-all">${esc((p.info || "").slice(0, 160))}</td>
          <td><button type="button" class="btn sm danger" data-sqlkill="${p.id}">KILL 单</button></td>
        </tr>`;
      });
      html += `</tbody></table></div>`;
    }
    html += `<div class="section-title" style="margin-top:16px"><span>锁等待</span><span class="tag">${locks.length}</span></div>`;
    if (!locks.length) html += `<div class="hint">当前无锁等待</div>`;
    else {
      html += locks.map(l => `<div class="ds-card" style="margin-bottom:8px"><div class="ds-info">
        <div class="ds-name">waiting ${l.waiting_pid || "?"} ← blocking ${l.blocking_pid || "?"}</div>
        <div class="hint mono">${esc((l.waiting_query || "").slice(0, 180))}</div>
        <div class="hint mono">${esc((l.blocking_query || "").slice(0, 180))}</div>
      </div></div>`).join("");
    }
    panel.innerHTML = html;
    panel.querySelectorAll("[data-sqlkill]").forEach(btn => {
      btn.onclick = async () => {
        const pid = btn.getAttribute("data-sqlkill");
        if (!confirm(`提交 KILL ${pid} 变更单？审批后才会执行`)) return;
        try {
          const cr = await submitSQLChangeRequest(id, `KILL ${pid}`, "terminate session", "kill");
          toast(`KILL 变更单 ${cr.id.slice(0, 8)} 已提交`, "ok");
          showSQLTab("changes");
        } catch (e) { toast(e.message || String(e), "err"); }
      };
    });
  } catch (e) {
    panel.innerHTML = `<div class="hint" style="color:var(--danger,#c00)">${esc(String(e.message || e))}</div>`;
  }
}

async function loadSQLSchemaHealth() {
  const sel = $("sqlProcessConnSel");
  const id = sel && sel.value;
  const panel = $("sqlProcessPanel");
  if (!id || !panel) { toast("请先选择连接", "err"); return; }
  panel.innerHTML = `<div class="hint">Schema 健康检查中…</div>`;
  try {
    const r = await fetch(`${API}/sql/connections/${encodeURIComponent(id)}/schema/health`);
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || "failed");
    const findings = j.findings || [];
    if (!findings.length) {
      panel.innerHTML = `<div class="hint">未发现明显 Schema 健康问题（抽检项）</div>`;
      return;
    }
    panel.innerHTML = `<div class="section-title"><span>Schema 健康</span><span class="tag">${findings.length}</span></div>` +
      findings.map(f => `<div class="ds-card" style="margin-bottom:8px"><div class="ds-info">
        <div class="ds-name">[${esc(f.level)}] ${esc(f.title)} · ${esc(f.schema || "")}.${esc(f.table || "")}</div>
        <div class="hint">${esc(f.detail || "")}</div>
        <div class="hint">${esc(f.suggest || "")}</div>
      </div></div>`).join("");
  } catch (e) {
    panel.innerHTML = `<div class="hint" style="color:var(--danger,#c00)">${esc(String(e.message || e))}</div>`;
  }
}

function renderSQLSlowConnSelect() {
  const sel = $("sqlSlowConnSel");
  if (!sel) return;
  const prev = sel.value || ($("sqlConnSel") && $("sqlConnSel").value) || "";
  const enabled = SQL_CONNS.filter(c => c.enabled !== false);
  sel.innerHTML = enabled.map(c =>
    `<option value="${esc(c.id)}">[${esc(c.env || "prod")}] ${esc(c.name)}</option>`
  ).join("") || `<option value="">${esc(sqlT("sql.no_conn_enabled", "无可用连接"))}</option>`;
  if (prev && enabled.some(c => c.id === prev)) sel.value = prev;
  if (!sel.dataset.bound) {
    sel.dataset.bound = "1";
    sel.addEventListener("change", () => loadSQLSlowLatest());
  }
}

async function loadSQLSlowLatest() {
  const sel = $("sqlSlowConnSel");
  const id = sel && sel.value;
  const panel = $("sqlSlowPanel");
  const meta = $("sqlSlowMeta");
  if (!id) {
    if (panel) panel.innerHTML = `<div class="hint">${esc(sqlT("sql.slow_need_conn", "请先添加并选择 MySQL 连接"))}</div>`;
    if (meta) meta.textContent = "";
    setSQLSlowFiltersVisible(false);
    SQL_SLOW_REPORT = null;
    return;
  }
  SQL_SLOW_PAGE = 1;
  if (panel) panel.innerHTML = `<div class="hint">${esc(sqlT("ui.loading", "加载中…"))}</div>`;
  try {
    const j = await fetch(`${API}/sql/connections/${encodeURIComponent(id)}/slow-sql/latest`).then(r => r.json());
    SQL_SLOW_REPORT = j.report || null;
    renderSQLSlowReport(SQL_SLOW_REPORT);
  } catch (e) {
    setSQLSlowFiltersVisible(false);
    if (panel) panel.innerHTML = `<div class="hint">${esc(String(e))}</div>`;
  }
}

async function selectSQLWorkbenchConn(connId, schema) {
  await loadSQLConnections();
  renderSQLConnSelect();
  const connSel = $("sqlConnSel");
  if (!connSel) return false;
  const id = String(connId || "").trim();
  if (!id) {
    toast(sqlT("sql.need_conn", "请先选择数据库连接"), "err");
    return false;
  }
  const c = sqlConnById(id);
  if (!c) {
    toast(sqlT("sql.conn_missing", "连接不存在或已删除，请刷新后重选"), "err");
    return false;
  }
  if (c.enabled === false) {
    toast(sqlT("sql.conn_disabled", "连接已停用，请在「连接管理」中启用"), "err");
    return false;
  }
  connSel.value = id;
  syncSQLDialectFromConn(id);
  syncSQLWorkbenchForDriver(id);
  const db = (schema || c.database || "").trim();
  await loadSQLSchema(id, "", db || "");
  if (db) setActiveSQLDatabase(db, { inferred: !!schema && schema !== c.database });
  syncSQLDbSelect(id, db || SQL_SCHEMA.database || "");
  return true;
}

async function applySlowSQLToWorkbench(it) {
  if (!it) return;
  showSQLTab("workbench");
  const slowSel = $("sqlSlowConnSel");
  const connId = (slowSel && slowSel.value) || (SQL_SLOW_REPORT && SQL_SLOW_REPORT.connection_id) || "";
  let schema = (it.schema || "").trim();
  if (!schema) {
    schema = inferSchemaFromSQLClient(it.sql || "");
    if (schema) it.schema_inferred = true;
  }
  const ok = await selectSQLWorkbenchConn(connId, schema);
  if (!ok) return;
  const connSel = $("sqlConnSel");
  if (connSel && connId) connSel.value = connId;
  if (schema) setActiveSQLDatabase(schema, { inferred: !!it.schema_inferred });
  setSQLText(String(it.sql || "").trim());
  const stillPH = !!it.params_unresolved || sqlHasDigestPlaceholders(it.sql);
  const truncated = !!it.sql_truncated;
  const recovered = !!it.sql_recovered && !truncated && !stillPH;
  const banner = $("sqlEditorHint");
  if (banner) {
    const notes = [];
    if (schema) notes.push((it.schema_inferred ? "推断库 " : "库 ") + schema);
    else notes.push("未识别库名：请在上方「库 / Schema」下拉框中选择后再 EXPLAIN");
    if (recovered) notes.push("已从语句历史/缓存还原真实参数" + (it.recovery_source ? "（" + it.recovery_source + "）" : ""));
    else if (it.sql_recovered && stillPH) notes.push("已尝试恢复，但仍含 DIGEST 占位");
    if (stillPH && !truncated) notes.push("仍为 DIGEST 摘要（含 '?'），未能还原真实参数；EXPLAIN 将使用探测值");
    if (truncated) notes.push("SQL 仍被 performance_schema 截断，无法 EXPLAIN；请粘贴完整语句或提高限额后重采");
    banner.textContent = notes.length ? notes.join(" · ") : "";
    banner.style.display = notes.length ? "" : "none";
    banner.className = "hint" + (truncated || stillPH || !schema ? " sql-trunc-warn" : "");
  }
  renderSQLTruncActions({ truncated, stillPH, connId });
  if (truncated) {
    toast(sqlT("sql.truncated_warn", "慢 SQL 仍截断：请粘贴完整语句，或复制调参 SQL 提高限额后重采"), "err");
  } else if (stillPH) {
    toast(sqlT("sql.params_unresolved", "仍为 DIGEST 摘要：未能从语句历史还原真实参数；EXPLAIN 将使用探测值"), "warn");
  } else if (!schema) {
    toast(sqlT("sql.schema_needed", "已填入 SQL，但未识别到库名——请在上方「库 / Schema」中选择后再 EXPLAIN"), "err");
  } else {
    toast(sqlT("sql.applied", "已应用") + (it.sql_recovered ? " · 已还原全文" : "") + " · " + schema, "ok");
  }
}

/** Detect MySQL DIGEST_TEXT placeholders: unbound ? / $n, or string literals that are exactly '?'. */
function sqlHasDigestPlaceholders(sql) {
  const s = String(sql || "");
  if (!s) return false;
  if (s.includes("'?'") || s.includes('"?"')) return true;
  if (/\$\d+\b/.test(s)) return true;
  // Strip quoted/backtick spans, then look for bare ?
  const stripped = s.replace(/'(?:\\.|''|[^'\\])*'|"(?:\\.|""|[^"\\])*"|`(?:``|[^`])*`/g, " ");
  return /(^|[^:$])\?(?!\w)/.test(stripped);
}

function renderSQLTruncActions(opts) {
  const box = $("sqlTruncActions");
  if (!box) return;
  const truncated = !!(opts && opts.truncated);
  const connId = (opts && opts.connId) || "";
  if (!truncated) {
    box.hidden = true;
    box.style.display = "none";
    box.innerHTML = "";
    return;
  }
  const lim = (SQL_SLOW_REPORT && SQL_SLOW_REPORT.ps_limits) || null;
  const hasRemedy = lim && lim.remedy_sql;
  box.hidden = false;
  box.style.display = "flex";
  box.innerHTML = `
    <button type="button" class="btn sm primary" id="sqlPasteFullFocusBtn">${esc(sqlT("sql.paste_full", "粘贴完整 SQL"))}</button>
    <button type="button" class="btn sm" id="sqlCopyRemedyBtn" ${hasRemedy ? "" : "disabled"}>${esc(sqlT("sql.copy_remedy", "复制调参 SQL"))}</button>
    <button type="button" class="btn sm ghost" id="sqlApplyRemedyBtn" ${connId ? "" : "disabled"}>${esc(sqlT("sql.apply_remedy", "尝试写入限额"))}</button>
  `;
  const focusBtn = $("sqlPasteFullFocusBtn");
  if (focusBtn) focusBtn.onclick = () => {
    const ed = $("sqlEditor");
    if (ed) { ed.focus(); ed.select(); }
    toast(sqlT("sql.paste_hint", "请粘贴完整 SQL 后重新 EXPLAIN"), "ok");
  };
  const copyBtn = $("sqlCopyRemedyBtn");
  if (copyBtn) copyBtn.onclick = async () => {
    const sql = (lim && lim.remedy_sql) || "";
    if (!sql) return;
    try {
      await navigator.clipboard.writeText(sql);
      toast(sqlT("sql.remedy_copied", "已复制调参 SQL（写入后多数版本需重启 mysqld）"), "ok");
    } catch (_) {
      toast(sql, "ok");
    }
  };
  const applyBtn = $("sqlApplyRemedyBtn");
  if (applyBtn) applyBtn.onclick = () => applySlowSQLPSLimits(connId);
}

async function applySlowSQLPSLimits(connId) {
  if (!connId) return;
  const ok = typeof uiConfirm === "function"
    ? await uiConfirm({
        title: sqlT("sql.apply_remedy_title", "提高 P_S 文本限额"),
        message: sqlT("sql.apply_remedy_confirm", "将尝试 SET PERSIST 提高 max_digest_length / SQL_TEXT 限额到 8192。多数版本仍需重启 mysqld 后生效。继续？"),
        detail: sqlT("sql.apply_remedy_detail", "需要目标库具备相应权限；写入失败时可复制调参 SQL 手工执行。"),
        confirmText: sqlT("sql.apply_remedy", "尝试写入限额"),
        tone: "warn"
      })
    : confirm(sqlT("sql.apply_remedy_confirm", "将尝试 SET PERSIST 提高 max_digest_length / SQL_TEXT 限额到 8192。多数版本仍需重启 mysqld 后生效。继续？"));
  if (!ok) return;
  try {
    const r = await fetch(`${API}/sql/connections/${encodeURIComponent(connId)}/slow-sql/ps-limits/apply`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ confirm: true, target: 8192 })
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) { toast(j.error || "写入失败", "err"); return; }
    if (SQL_SLOW_REPORT && j.limits) SQL_SLOW_REPORT.ps_limits = j.limits;
    const notes = (j.notes || []).join("；");
    toast((notes || "已提交") + (j.restart ? " · " + j.restart : ""), notes && notes.indexOf("失败") >= 0 ? "warn" : "ok");
    renderSQLSlowPSLimitsBanner(SQL_SLOW_REPORT);
  } catch (e) { toast(String(e), "err"); }
}

function renderSQLSlowPSLimitsBanner(rep) {
  const meta = $("sqlSlowMeta");
  let box = $("sqlSlowPSLimits");
  if (!box) {
    box = document.createElement("div");
    box.id = "sqlSlowPSLimits";
    box.className = "sql-ps-limits";
    if (meta && meta.parentNode) meta.parentNode.insertBefore(box, meta.nextSibling);
    else {
      const panel = $("sqlSlowPanel");
      if (panel && panel.parentNode) panel.parentNode.insertBefore(box, panel);
    }
  }
  const lim = rep && rep.ps_limits;
  if (!lim) {
    box.style.display = "none";
    box.innerHTML = "";
    return;
  }
  box.style.display = "";
  const connId = (rep && rep.connection_id) || (($("sqlSlowConnSel") && $("sqlSlowConnSel").value) || "");
  box.innerHTML = `
    <div><b>P_S 文本限额</b> · max_digest_length=${esc(String(lim.max_digest_length || "—"))}
 · SQL_TEXT=${esc(String(lim.performance_schema_max_sql_text_length || "—"))}</div>
    <div class="hint" style="margin:4px 0 0">${esc(lim.remedy_note || "")}</div>
    <div class="sql-ps-limits-acts">
      <button type="button" class="btn sm" id="sqlSlowCopyRemedyBtn" ${lim.remedy_sql ? "" : "disabled"}>${esc(sqlT("sql.copy_remedy", "复制调参 SQL"))}</button>
      <button type="button" class="btn sm ghost" id="sqlSlowApplyRemedyBtn" ${connId ? "" : "disabled"}>${esc(sqlT("sql.apply_remedy", "尝试写入限额"))}</button>
    </div>
  `;
  const copyBtn = $("sqlSlowCopyRemedyBtn");
  if (copyBtn) copyBtn.onclick = async () => {
    try {
      await navigator.clipboard.writeText(lim.remedy_sql || "");
      toast(sqlT("sql.remedy_copied", "已复制调参 SQL（写入后多数版本需重启 mysqld）"), "ok");
    } catch (_) { toast(lim.remedy_sql || "", "ok"); }
  };
  const applyBtn = $("sqlSlowApplyRemedyBtn");
  if (applyBtn) applyBtn.onclick = () => applySlowSQLPSLimits(connId);
}

/** Infer schema.table qualifiers from SQL text (client-side, mirrors server). */
function inferSchemaFromSQLClient(sql) {
  const text = String(sql || "");
  const re = /(?:from|join|update|into|table)\s+(?:`([a-zA-Z0-9_]+)`|([a-zA-Z0-9_]+))\s*\.\s*(?:`([a-zA-Z0-9_]+)`|([a-zA-Z0-9_]+))/ig;
  const counts = {};
  let m, best = "", bestN = 0;
  const skip = { mysql:1, information_schema:1, performance_schema:1, sys:1 };
  while ((m = re.exec(text))) {
    const s = (m[1] || m[2] || "").trim();
    if (!s || skip[s.toLowerCase()]) continue;
    counts[s] = (counts[s] || 0) + 1;
    if (counts[s] > bestN) { best = s; bestN = counts[s]; }
  }
  return best;
}

/** Classify SQL into select|insert|update|delete|other for Slow SQL filters. */
function classifySlowSQLKind(sql) {
  let s = String(sql || "").trim();
  // strip leading comments / parentheses
  for (let i = 0; i < 8; i++) {
    const n = s.replace(/^\/\*[\s\S]*?\*\//, "").replace(/^--[^\n]*/, "").replace(/^\(+/, "").trim();
    if (n === s) break;
    s = n;
  }
  const m = s.match(/^(WITH|SELECT|INSERT|REPLACE|UPDATE|DELETE|CREATE|ALTER|DROP|TRUNCATE|CALL|SHOW|SET|EXPLAIN|MERGE)\b/i);
  if (!m) return "other";
  const k = m[1].toUpperCase();
  if (k === "SELECT" || k === "WITH" || k === "SHOW" || k === "EXPLAIN") return "select";
  if (k === "INSERT" || k === "REPLACE") return "insert";
  if (k === "UPDATE" || k === "MERGE") return "update";
  if (k === "DELETE" || k === "TRUNCATE") return "delete";
  return "other";
}

function slowSQLKindLabel(kind) {
  const map = {
    select: sqlT("sql.slow_kind_select", "查询"),
    insert: sqlT("sql.slow_kind_insert", "写入"),
    update: sqlT("sql.slow_kind_update", "更新"),
    delete: sqlT("sql.slow_kind_delete", "删除"),
    other: sqlT("sql.slow_kind_other", "其它")
  };
  return map[kind] || map.other;
}

function fmtSlowMs(ms) {
  const n = Number(ms) || 0;
  if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 0 : 1) + " s";
  return n.toFixed(n >= 100 ? 0 : 1) + " ms";
}

function setSQLSlowFiltersVisible(on) {
  const sum = $("sqlSlowSummary");
  const fil = $("sqlSlowFilters");
  if (sum) sum.style.display = on ? "" : "none";
  if (fil) fil.style.display = on ? "" : "none";
}

function syncSQLSlowFilterControls() {
  const search = $("sqlSlowSearch");
  if (search && search.value !== SQL_SLOW_Q) search.value = SQL_SLOW_Q;
  const sort = $("sqlSlowSort");
  if (sort && sort.value !== SQL_SLOW_SORT) sort.value = SQL_SLOW_SORT;
  const chips = $("sqlSlowTypeFilter");
  if (chips) {
    chips.querySelectorAll("[data-slow-type]").forEach(btn => {
      btn.classList.toggle("active", (btn.dataset.slowType || "") === SQL_SLOW_TYPE);
    });
  }
}

function buildSQLSlowView(items) {
  const q = (SQL_SLOW_Q || "").trim().toLowerCase();
  const type = SQL_SLOW_TYPE || "";
  let rows = (items || []).map((it, idx) => {
    const kind = classifySlowSQLKind(it && it.sql);
    return { it, idx, kind, schema: (it && it.schema) || "(unknown)" };
  });
  if (type) rows = rows.filter(r => r.kind === type);
  if (q) {
    rows = rows.filter(r => {
      const it = r.it || {};
      const hay = `${it.sql || ""} ${it.schema || ""} ${it.digest || ""} ${it.recovery_source || ""}`.toLowerCase();
      return hay.includes(q);
    });
  }
  const cmpNum = (a, b) => (b - a) || 0;
  rows.sort((a, b) => {
    const x = a.it || {}, y = b.it || {};
    switch (SQL_SLOW_SORT) {
      case "sum_desc": return cmpNum(Number(x.sum_latency_ms || 0), Number(y.sum_latency_ms || 0));
      case "count_desc": return cmpNum(Number(x.count_star || 0), Number(y.count_star || 0));
      case "score_desc": return cmpNum(Number(x.score || 0), Number(y.score || 0));
      case "len_desc": return cmpNum(String(x.sql || "").length, String(y.sql || "").length);
      case "avg_desc":
      default: return cmpNum(Number(x.avg_latency_ms || 0), Number(y.avg_latency_ms || 0));
    }
  });
  return rows;
}

function renderSQLSlowSummary(allItems, view) {
  const box = $("sqlSlowSummary");
  if (!box) return;
  const total = (allItems || []).length;
  const n = (view || []).length;
  let maxAvg = 0, sumLat = 0, sumCnt = 0;
  const kindCnt = { select: 0, insert: 0, update: 0, delete: 0, other: 0 };
  (allItems || []).forEach(it => {
    const k = classifySlowSQLKind(it && it.sql);
    kindCnt[k] = (kindCnt[k] || 0) + 1;
  });
  (view || []).forEach(r => {
    const it = r.it || {};
    maxAvg = Math.max(maxAvg, Number(it.avg_latency_ms || 0));
    sumLat += Number(it.sum_latency_ms || 0);
    sumCnt += Number(it.count_star || 0);
  });
  const filteredNote = n !== total
    ? sqlT("sql.slow_filtered_of", "筛选 {n}/{total}").replace("{n}", String(n)).replace("{total}", String(total))
    : String(total);
  box.innerHTML = `
    <div class="sec-stat"><div class="sec-stat-n">${esc(filteredNote)}</div><div class="sec-stat-l">${esc(sqlT("sql.slow_stat_count", "慢语句"))}</div></div>
    <div class="sec-stat"><div class="sec-stat-n">${esc(fmtSlowMs(maxAvg))}</div><div class="sec-stat-l">${esc(sqlT("sql.slow_stat_max_avg", "最高均耗"))}</div></div>
    <div class="sec-stat"><div class="sec-stat-n">${esc(fmtSlowMs(sumLat))}</div><div class="sec-stat-l">${esc(sqlT("sql.slow_stat_sum", "累计耗时"))}</div></div>
    <div class="sec-stat"><div class="sec-stat-n">${esc(String(sumCnt))}</div><div class="sec-stat-l">${esc(sqlT("sql.slow_stat_exec", "执行次数"))}</div></div>
    <div class="sec-stat"><div class="sec-stat-n" style="font-size:14px;line-height:1.35">${esc(`查${kindCnt.select} · 写${kindCnt.insert} · 更${kindCnt.update} · 删${kindCnt.delete}`)}</div><div class="sec-stat-l">${esc(sqlT("sql.slow_stat_kinds", "类型分布（全量）"))}</div></div>
  `;
  const chips = $("sqlSlowTypeFilter");
  if (chips) {
    chips.querySelectorAll("[data-slow-type]").forEach(btn => {
      const t = btn.dataset.slowType || "";
      const base = t === "" ? sqlT("sql.slow_kind_all", "全部")
        : t === "select" ? sqlT("sql.slow_kind_select", "查询")
        : t === "insert" ? sqlT("sql.slow_kind_insert", "写入")
        : t === "update" ? sqlT("sql.slow_kind_update", "更新")
        : t === "delete" ? sqlT("sql.slow_kind_delete", "删除")
        : sqlT("sql.slow_kind_other", "其它");
      const c = t === "" ? total : (kindCnt[t] || 0);
      btn.textContent = `${base} ${c}`;
    });
  }
}

function renderSQLSlowReport(rep) {
  const panel = $("sqlSlowPanel");
  const meta = $("sqlSlowMeta");
  if (!panel) return;
  if (!rep) {
    if (meta) meta.textContent = "";
    setSQLSlowFiltersVisible(false);
    SQL_SLOW_VIEW = [];
    panel.innerHTML = `<div class="hint">${esc(sqlT("sql.slow_empty", "尚无报告。点击「立即检查」从 performance_schema 拉取全库慢 SQL。"))}</div>`;
    return;
  }
  const when = rep.finished_at ? new Date(rep.finished_at * 1000).toLocaleString() : (rep.started_at ? new Date(rep.started_at * 1000).toLocaleString() : "—");
  if (meta) {
    let t = `${rep.connection_name || rep.connection_id} · ${rep.status} · ${rep.trigger || "—"} · ${when} · ${rep.item_count || 0} 条`;
    if (rep.trend) {
      t += ` · 趋势 +${rep.trend.new_digests || 0}/-${rep.trend.gone_digests || 0} 恶化${rep.trend.worsened || 0}`;
    }
    meta.textContent = t;
  }
  renderSQLSlowPSLimitsBanner(rep);
  if (rep.status === "failed") {
    setSQLSlowFiltersVisible(false);
    panel.innerHTML = `<div class="hint" style="color:var(--danger,#c00)">${esc(rep.error || "采集失败")}</div>`;
    return;
  }
  if (rep.status === "running") {
    setSQLSlowFiltersVisible(false);
    panel.innerHTML = `<div class="hint">${esc(sqlT("sql.slow_running", "正在检查…"))}</div>`;
    return;
  }
  const items = Array.isArray(rep.items) ? rep.items : [];
  if (!items.length) {
    setSQLSlowFiltersVisible(false);
    panel.innerHTML = `<div class="hint">${esc(sqlT("sql.slow_none", "未发现达到阈值的慢语句摘要"))}</div>`;
    return;
  }
  setSQLSlowFiltersVisible(true);
  SQL_SLOW_VIEW = buildSQLSlowView(items);
  renderSQLSlowSummary(items, SQL_SLOW_VIEW);
  syncSQLSlowFilterControls();
  SQL_SLOW_PAGE = tblClampPage(SQL_SLOW_PAGE, SQL_SLOW_VIEW.length, SQL_SLOW_SIZE);

  const trendBadge = (tr) => {
    if (tr === "new") return `<span class="badge warn">NEW</span>`;
    if (tr === "worse") return `<span class="badge crit">WORSE</span>`;
    if (tr === "better") return `<span class="badge ok">BETTER</span>`;
    return "";
  };
  let html = "";
  if (rep.trend) {
    html += `<div class="hint" style="margin-bottom:8px">较上次报告：新增 ${rep.trend.new_digests || 0} · 消失 ${rep.trend.gone_digests || 0} · 恶化 ${rep.trend.worsened || 0} · 改善 ${rep.trend.improved || 0}</div>`;
  }
  if (!SQL_SLOW_VIEW.length) {
    html += `<div class="hint">${esc(sqlT("sql.slow_filter_empty", "当前筛选无匹配项，请调整搜索或类型"))}</div>`;
    panel.innerHTML = html;
    return;
  }
  const start = (SQL_SLOW_PAGE - 1) * SQL_SLOW_SIZE;
  const pageRows = SQL_SLOW_VIEW.slice(start, start + SQL_SLOW_SIZE);
  let lastSchema = null;
  pageRows.forEach(row => {
    const { it, idx, kind, schema } = row;
    if (schema !== lastSchema) {
      lastSchema = schema;
      const schemaLabel = schema === "(unknown)"
        ? sqlT("sql.schema_unknown", "未识别库（填入后将尝试推断）")
        : schema;
      const schemaCount = SQL_SLOW_VIEW.filter(r => r.schema === schema).length;
      html += `<div class="section-title" style="margin:14px 0 6px"><span>${esc(schemaLabel)}</span><span class="tag">${schemaCount}</span></div>`;
    }
    const tip = (it.index_hints && it.index_hints[0] && (it.index_hints[0].ddl || it.index_hints[0].reason || it.index_hints[0].message)) ||
      (it.suggestions && it.suggestions[0] && (it.suggestions[0].title || it.suggestions[0].detail)) ||
      (it.findings && it.findings[0] && (it.findings[0].title || it.findings[0].detail)) ||
      sqlT("sql.slow_no_tip", "暂无规则建议");
    const flags = [];
    flags.push(`<span class="badge">${esc(slowSQLKindLabel(kind))}</span>`);
    if (it.schema_inferred) flags.push(`<span class="badge">库推断</span>`);
    if (it.sql_recovered && !it.sql_truncated) flags.push(`<span class="badge ok">${esc(sqlT("sql.recovered_full", "已还原全文"))}</span>`);
    else if (it.sql_recovered) flags.push(`<span class="badge ok">已恢复</span>`);
    if (it.params_unresolved) flags.push(`<span class="badge warn">${sqlT("sql.params_badge", "参数未还原")}</span>`);
    if (it.sql_truncated) flags.push(`<span class="badge crit">${esc(sqlT("sql.still_truncated", "文本仍截断"))}</span>`);
    if (it.recovery_source) flags.push(`<span class="badge">${esc(it.recovery_source)}</span>`);
    const sqlPreview = it.sql || "";
    html += `<div class="ds-card" style="margin-bottom:8px">
      <div class="ds-info" style="flex:1;min-width:0">
        <div class="ds-name mono" style="white-space:pre-wrap;word-break:break-all">${trendBadge(it.trend)} ${flags.join(" ")} ${esc(sqlPreview.slice(0, 400))}${sqlPreview.length > 400 ? "…" : ""}</div>
        <div class="ds-url"><span>avg ${Number(it.avg_latency_ms || 0).toFixed(1)} ms · sum ${Number(it.sum_latency_ms || 0).toFixed(0)} ms · ×${it.count_star || 0} · score ${it.score ?? "—"} · ${sqlPreview.length} 字符</span></div>
        <div class="hint" style="margin-top:4px">${esc(String(tip).slice(0, 180))}</div>
      </div>
      <div class="ds-actions">
        <button type="button" class="btn sm" data-sqlslow="use" data-idx="${idx}">${esc(sqlT("sql.slow_use", "填入工作台"))}</button>
        <button type="button" class="btn sm ai-assist-btn" data-sqlslow="ai" data-idx="${idx}"><span class="ai-assist-btn-ic">🤖</span>${esc(sqlT("sql.ai_optimize_short", "AI 深度优化"))}</button>
      </div>
    </div>`;
  });
  html += tblPager(SQL_SLOW_VIEW.length, SQL_SLOW_PAGE, SQL_SLOW_SIZE);
  panel.innerHTML = html;
  panel.querySelectorAll("[data-sqlslow]").forEach(btn => {
    btn.onclick = async () => {
      const idx = parseInt(btn.dataset.idx, 10);
      const it = items[idx];
      if (!it) return;
      await applySlowSQLToWorkbench(it);
      if (btn.dataset.sqlslow === "ai") openSQLAI("sql_remediation");
    };
  });
}

function refreshSQLSlowList() {
  if (SQL_SLOW_REPORT) renderSQLSlowReport(SQL_SLOW_REPORT);
}

async function runSQLSlowCheck() {
  const sel = $("sqlSlowConnSel");
  const id = sel && sel.value;
  if (!id) { toast(sqlT("sql.slow_need_conn", "请先选择连接"), "err"); return; }
  await withLoading("sqlSlowRunBtn", async () => {
    try {
      const r = await fetch(`${API}/sql/connections/${encodeURIComponent(id)}/slow-sql/run`, { method: "POST" });
      const j = await r.json().catch(() => ({}));
      if (r.status === 409) { toast(j.error || "检查进行中", "err"); return; }
      if (!r.ok && !j.status) { toast(j.error || "检查失败", "err"); return; }
      SQL_SLOW_PAGE = 1;
      SQL_SLOW_REPORT = j;
      renderSQLSlowReport(j);
      if (j.status === "failed") toast(j.error || "采集失败", "err");
      else toast(sqlT("sql.slow_done", "慢 SQL 检查完成"), "ok");
    } catch (e) { toast(String(e), "err"); }
  });
}

async function loadSQLChangeRequests() {
  try {
    const r = await fetch(`${API}/sql/change-requests`);
    const j = await r.json().catch(() => ({}));
    SQL_CHANGES = r.ok && Array.isArray(j.change_requests) ? j.change_requests : [];
  } catch (_) {
    SQL_CHANGES = [];
  }
}

function sqlConnectionEnvironment(id) {
  const c = SQL_CONNS.find(x => x.id === id);
  return c && c.env ? c.env : "prod";
}
window.sqlConnectionEnvironment = sqlConnectionEnvironment;

async function submitSQLChangeRequest(connectionId, sql, reason, kind) {
  const r = await fetch(`${API}/sql/change-requests`, {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ connection_id: connectionId, sql, reason: reason || "", kind: kind || "" })
  });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || "提交变更单失败");
  await loadSQLChangeRequests();
  renderSQLChangeList();
  return j;
}
window.submitSQLChangeRequest = submitSQLChangeRequest;

async function submitSQLChangeFromEditor() {
  const connectionId = $("sqlConnSel") && $("sqlConnSel").value;
  const sql = sqlText().trim();
  if (!connectionId || !sql) { toast("请选择连接并输入 DDL", "err"); return; }
  const reason = prompt("请输入变更原因（可选）", "") || "";
  try {
    const cr = await submitSQLChangeRequest(connectionId, sql, reason);
    toast(`变更单 ${cr.id.slice(0, 8)} 已提交`, "ok");
    showSQLTab("changes");
  } catch (e) { toast(e.message || String(e), "err"); }
}

function renderSQLChangeList() {
  const list = $("sqlChangeList");
  if (!list) return;
  if (!SQL_CHANGES.length) {
    list.innerHTML = `<div class="ds-empty">暂无 DDL 变更单</div>`;
    return;
  }
  const admin = typeof isAdmin === "function" && isAdmin();
  const writable = typeof canWrite === "function" && canWrite();
  list.innerHTML = SQL_CHANGES.map(cr => {
    const expires = cr.expires_at ? new Date(cr.expires_at * 1000).toLocaleString() : "—";
    const approve = admin && cr.status === "pending"
      ? `<button class="btn sm" data-sqlchange="approve" data-id="${esc(cr.id)}">批准</button>
         <button class="btn danger sm" data-sqlchange="reject" data-id="${esc(cr.id)}">驳回</button>` : "";
    const rejectApproved = admin && cr.status === "approved"
      ? `<button class="btn danger sm" data-sqlchange="reject" data-id="${esc(cr.id)}">撤销</button>` : "";
    const execute = writable && cr.status === "approved"
      ? `<button class="btn primary sm" data-sqlchange="execute" data-id="${esc(cr.id)}">执行一次</button>` : "";
    return `<div class="ds-card" data-id="${esc(cr.id)}">
      <div class="ds-type-icon">DDL</div>
      <div class="ds-info">
        <div class="ds-name">[${esc(cr.environment || "prod")}] ${esc(cr.connection_name || cr.connection_id)}
          <span class="tag">${esc(cr.status)}</span>${cr.change_id?` <span class="tag" title="通用变更记录">CHG #${esc(String(cr.change_id))}</span>`:""}</div>
        <div class="ds-url"><span>${esc(cr.proposer || "")} · ${new Date(cr.created_at * 1000).toLocaleString()}</span>
          <span class="ds-auth">有效期 ${esc(expires)}</span></div>
        <pre class="mono sql-snippet">${esc(cr.sql || "")}</pre>
        ${cr.reason ? `<div class="hint">原因：${esc(cr.reason)}</div>` : ""}
        ${cr.error ? `<div class="hint" style="color:var(--danger)">失败：${esc(cr.error)}</div>` : ""}
      </div>
      <div class="ds-actions">${approve}${rejectApproved}${execute}</div>
    </div>`;
  }).join("");
}

async function actSQLChange(id, action) {
  const promptText = action === "execute" ? "确认执行该 DDL？审批票将立即且永久消耗。" :
    (action === "approve" ? "确认批准该 DDL 变更单？" : "确认驳回/撤销该变更单？");
  if (!confirm(promptText)) return;
  try {
    const verifySQL = action === "execute" ? pickVerifySQL() : "";
    const body = action === "execute" && verifySQL ? { verify_sql: verifySQL } : {};
    const r = await fetch(`${API}/sql/change-requests/${encodeURIComponent(id)}/${action}`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body)
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error || "操作失败");
    toast(action === "execute" ? "DDL 已执行" : "变更单已更新", "ok");
    if (action === "execute" && (j.result || j.explain_diff || j.explain_before)) {
      showSQLExplainDiffResult(j.result || j);
    }
    await loadSQLChangeRequests();
    renderSQLChangeList();
    const conn = $("sqlConnSel") && $("sqlConnSel").value;
    if (conn) loadSQLSchema(conn);
  } catch (e) { toast(e.message || String(e), "err"); }
}

function sqlDialect() {
  const el = $("sqlDialect");
  return el ? el.value : "mysql80";
}

function sqlText() {
  const el = $("sqlEditor");
  return el ? el.value : "";
}

function setSQLText(v) {
  const el = $("sqlEditor");
  if (el) el.value = v;
}
window.setSQLText = setSQLText;

async function runSQLAnalyze() {
  const sql = sqlText().trim();
  if (!sql) { toast(sqlT("sql.empty", "请先输入 SQL"), "err"); return; }
  await withLoading("sqlAnalyzeBtn", async () => {
    try {
      const body = { sql, dialect: sqlDialect() };
      const conn = $("sqlConnSel") && $("sqlConnSel").value;
      if (conn) {
        await loadSQLConnections();
        const keep = conn;
        renderSQLConnSelect();
        if ($("sqlConnSel")) $("sqlConnSel").value = keep;
        const c = sqlConnById(keep);
        if (!c) { toast(sqlT("sql.conn_missing", "连接不存在或已删除，请刷新后重选"), "err"); return; }
        if (c.enabled === false) { toast(sqlT("sql.conn_disabled", "连接已停用，请在「连接管理」中启用"), "err"); return; }
        body.connection_id = keep;
        const schema = sqlActiveSchema() || c.database || inferSchemaFromSQLClient(sql) || "";
        if (schema) { body.schema = schema; body.database = schema; setActiveSQLDatabase(schema); }
      }
      const r = await fetch(`${API}/sql/analyze`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body)
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) { toast(j.error || "分析失败", "err"); return; }
      SQL_LAST.audit = { findings: j.findings, score: j.score };
      SQL_LAST.optimize = { rewritten_sql: j.rewritten_sql, suggestions: j.suggestions, index_hints: j.index_hints };
      SQL_LAST.explain = j.explain ? { analysis: j.explain } : null;
      SQL_LAST.analyze = j;
      if (/^\s*(select|with)\b/i.test(sql)) SQL_VERIFY_SQL = sql;
      renderSQLAnalyze(j);
      await loadSQLHistory();
      renderSQLHistory();
    } catch (e) { toast(String(e), "err"); }
  });
}

function renderExplainDiffHTML(payload) {
  if (!payload) return "";
  const diff = payload.explain_diff;
  const before = payload.explain_before;
  const after = payload.explain_after;
  if (!diff && !before && !after) return "";
  const changes = diff && Array.isArray(diff.changes) ? diff.changes : [];
  const rows = changes.map(c => `<tr>
    <td>${esc(c.table || "")}</td><td>${esc(c.field || "")}</td>
    <td>${esc(c.before || "—")}</td><td>${esc(c.after || "—")}</td>
  </tr>`).join("");
  const renderSide = (label, a) => {
    if (!a) return "";
    const hits = Array.isArray(a.table_access) ? a.table_access : [];
    const tr = hits.map(h => `<tr><td>${esc(h.table || "")}</td><td>${esc(h.access_type || "")}</td><td>${esc(h.key || "—")}</td><td>${esc(String(h.rows != null ? h.rows : ""))}</td></tr>`).join("");
    return `<div class="sql-opt-block"><div class="sql-opt-head">${esc(label)}</div>
      <div class="table-wrap"><table class="data sql-explain-table"><thead><tr><th>table</th><th>type</th><th>key</th><th>rows</th></tr></thead><tbody>${tr || "<tr><td colspan=4>—</td></tr>"}</tbody></table></div></div>`;
  };
  return `<div class="sql-explain-diff">
    <div class="sql-opt-head">DDL 后 EXPLAIN 对比</div>
    <div class="sql-explain-summary">${esc((diff && diff.summary) || "")}</div>
    ${rows ? `<div class="table-wrap"><table class="data sql-explain-table"><thead><tr><th>table</th><th>field</th><th>before</th><th>after</th></tr></thead><tbody>${rows}</tbody></table></div>` : ""}
    ${renderSide("Before", before)}${renderSide("After", after)}
  </div>`;
}

function showSQLExplainDiffResult(payload) {
  const merged = Object.assign({}, SQL_LAST.analyze || {}, {
    explain_before: payload.explain_before,
    explain_after: payload.explain_after,
    explain_diff: payload.explain_diff,
    explain: payload.explain_after || (SQL_LAST.analyze && SQL_LAST.analyze.explain),
  });
  SQL_LAST.analyze = merged;
  renderSQLAnalyze(merged);
  showSQLTab("workbench");
}
window.showSQLExplainDiffResult = showSQLExplainDiffResult;

function renderSQLAnalyze(j) {
  const box = $("sqlResultPanel");
  if (!box) return;
  const bd = j.score_breakdown || {};
  const findings = Array.isArray(j.findings) ? j.findings : [];
  const hints = Array.isArray(j.index_hints) ? j.index_hints : [];
  const findingRows = findings.map(f => {
    const lv = f.level || "info";
    return `<div class="sql-finding ${esc(lv)}">
      <div class="sql-finding-head"><span class="sql-lv">${esc(lv)}</span><strong>${esc(f.title || f.id)}</strong><code class="mono">${esc(f.id || "")}</code></div>
      <div class="sql-finding-detail">${esc(f.detail || "")}</div>
      ${f.suggest ? `<div class="sql-finding-suggest">${esc(f.suggest)}</div>` : ""}
    </div>`;
  }).join("") || `<div class="hint">${esc(sqlT("sql.no_findings", "未发现问题"))}</div>`;
  const hintRows = hints.map(h =>
    `<li><strong>${esc(h.table || "")}</strong> (${esc((h.columns || []).join(", "))})
     — ${esc(h.reason || "")}${h.meta ? ' <span class="tag">meta</span>' : ""}
     ${h.ddl ? `<pre class="mono sql-snippet">${esc(h.ddl)}</pre>` : ""}</li>`
  ).join("");
  let explainHTML = "";
  if (j.explain) {
    const a = j.explain;
    const hits = Array.isArray(a.table_access) ? a.table_access : [];
    const rows = hits.map(h => `<tr>
      <td>${esc(h.table || "")}</td><td>${esc(h.access_type || "")}</td><td>${esc(h.key || "—")}</td>
      <td>${esc(String(h.rows != null ? h.rows : ""))}</td>
      <td>${h.full_scan_risk ? "⚠" : (h.key ? "✓" : "")}</td>
    </tr>`).join("");
    explainHTML = `<div class="sql-opt-block"><div class="sql-opt-head">EXPLAIN</div>
      <div class="sql-explain-summary">${esc(a.summary || "")}</div>
      <div class="table-wrap"><table class="data sql-explain-table">
        <thead><tr><th>table</th><th>type</th><th>key</th><th>rows</th><th></th></tr></thead>
        <tbody>${rows || "<tr><td colspan=5>—</td></tr>"}</tbody>
      </table></div></div>`;
  }
  const rewritten = j.rewritten_sql || "";
  box.innerHTML = `
    <div class="sql-score">${esc(sqlT("sql.score", "综合分"))}: <b>${j.score != null ? j.score : "-"}</b>
      <span class="tag">${j.parsed ? "AST" : "regex"}</span>
      ${j.metadata_used ? '<span class="tag">meta</span>' : ""}
      ${j.explain_used ? '<span class="tag">EXPLAIN</span>' : ""}
    </div>
    <div class="sql-breakdown hint">static −${bd.static_penalty || 0} · meta −${bd.meta_penalty || 0} · explain −${bd.explain_penalty || 0}</div>
    ${j.parse_error ? `<div class="hint">parse: ${esc(j.parse_error)}</div>` : ""}
    <div class="sql-opt-block"><div class="sql-opt-head">${esc(sqlT("sql.findings", "Findings"))}</div>${findingRows}</div>
    <div class="sql-opt-block"><div class="sql-opt-head">${esc(sqlT("sql.index_hints", "索引提示"))}</div><ul class="sql-ul">${hintRows || "<li>—</li>"}</ul></div>
    ${explainHTML}
    ${renderExplainDiffHTML(j)}
    <div class="sql-opt-block">
      <div class="sql-opt-head">
        <span>${esc(sqlT("sql.rewritten", "改写建议"))}</span>
        <button type="button" class="btn sm" id="sqlCopyRewritten">${esc(sqlT("sql.copy", "复制"))}</button>
        <button type="button" class="btn sm" id="sqlApplyRewritten">${esc(sqlT("sql.apply", "应用到编辑器"))}</button>
      </div>
      <pre class="mono sql-rewritten" id="sqlRewrittenBody">${esc(rewritten || "—")}</pre>
    </div>`;
  const copyBtn = $("sqlCopyRewritten");
  if (copyBtn) copyBtn.onclick = () => {
    if (!rewritten) return;
    navigator.clipboard.writeText(rewritten).then(() => toast(sqlT("sql.copied", "已复制"), "ok")).catch(() => toast("复制失败", "err"));
  };
  const applyBtn = $("sqlApplyRewritten");
  if (applyBtn) applyBtn.onclick = () => { if (rewritten) { setSQLText(rewritten); toast(sqlT("sql.applied", "已应用"), "ok"); } };
}

async function runSQLBeautify() {
  const sql = sqlText().trim();
  if (!sql) { toast(sqlT("sql.empty", "请先输入 SQL"), "err"); return; }
  await withLoading("sqlBeautifyBtn", async () => {
    try {
      const r = await fetch(`${API}/sql/beautify`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sql, dialect: sqlDialect() })
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) { toast(j.error || "失败", "err"); return; }
      setSQLText(j.sql || "");
      SQL_LAST.beautified = j.sql || "";
      toast(sqlT("sql.beautified", "已美化"), "ok");
    } catch (e) { toast(String(e), "err"); }
  });
}

async function runSQLAudit() {
  const sql = sqlText().trim();
  if (!sql) { toast(sqlT("sql.empty", "请先输入 SQL"), "err"); return; }
  await withLoading("sqlAuditBtn", async () => {
    try {
      const r = await fetch(`${API}/sql/audit`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sql, dialect: sqlDialect() })
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) { toast(j.error || "失败", "err"); return; }
      SQL_LAST.audit = j;
      renderSQLFindings(j);
    } catch (e) { toast(String(e), "err"); }
  });
}

async function runSQLOptimize() {
  const sql = sqlText().trim();
  if (!sql) { toast(sqlT("sql.empty", "请先输入 SQL"), "err"); return; }
  await withLoading("sqlOptimizeBtn", async () => {
    try {
      const r = await fetch(`${API}/sql/optimize`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ sql, dialect: sqlDialect() })
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) { toast(j.error || "失败", "err"); return; }
      SQL_LAST.optimize = j;
      renderSQLOptimize(j);
    } catch (e) { toast(String(e), "err"); }
  });
}

/* ---------- 查询执行：流式、可停、可翻页、可导出 ----------
 *
 * 老实现是"一把梭"：POST /query 等到整条查询跑完，把整个结果集一次性收下来再渲染。
 * 数据量一大或查询一慢，每一环都出问题——用户在跑完之前什么都看不到、没法中途放弃、
 * 关掉页面查询还在数据库上跑到底、行数写死 200 想多看一点都不行。
 *
 * 现在走 NDJSON 流式接口：列信息先到（表头立刻出来），行数据分批到（边到边画），
 * 结束行带上耗时统计。AbortController 挂在"停止"按钮上——中止请求会让服务端的 ctx
 * 取消，数据库那条查询随之被终止。
 */

let SQL_RUN_ABORT = null;          // 当前查询的 AbortController
let SQL_RUN_TIMER = null;          // 已用时计时器
let SQL_LAST_RUN = null;           // 最近一次运行的参数，供翻页/导出复用

function sqlRunLimit() {
  const el = $("sqlLimitSel");
  const n = parseInt((el && el.value) || "1000", 10);
  return Number.isFinite(n) && n > 0 ? n : 1000;
}
function sqlRunTimeout() {
  const el = $("sqlTimeoutSel");
  const n = parseInt((el && el.value) || "20", 10);
  return Number.isFinite(n) && n > 0 ? n : 20;
}

function sqlSetRunning(on, startedAt) {
  const runBtn = $("sqlRunBtn");
  const cancelBtn = $("sqlCancelBtn");
  const prog = $("sqlRunProgress");
  if (runBtn) runBtn.disabled = !!on;
  if (cancelBtn) cancelBtn.hidden = !on;
  if (prog) prog.hidden = !on;
  clearInterval(SQL_RUN_TIMER);
  SQL_RUN_TIMER = null;
  if (!on) return;
  const tick = () => {
    if (!prog) return;
    const sec = ((Date.now() - startedAt) / 1000).toFixed(1);
    const got = SQL_STREAM_ROWS ? SQL_STREAM_ROWS.length : 0;
    prog.textContent = sqlT("sql.running_for", "运行中") + ` ${sec}s · ` + got + " " + sqlT("sql.query_rows", "行");
  };
  tick();
  SQL_RUN_TIMER = setInterval(tick, 200);
}

function cancelSQLQuery() {
  if (SQL_RUN_ABORT) {
    try { SQL_RUN_ABORT.abort(); } catch (e) {}
  }
}
window.cancelSQLQuery = cancelSQLQuery;

let SQL_STREAM_ROWS = null;  // 当前流式结果的行缓冲（列式数组）
let SQL_STREAM_COLS = null;

/** 解析当前的连接 / 库，失败时给出可执行的提示并返回 null。 */
async function sqlResolveRunTarget(connId, sql) {
  await loadSQLConnections();
  renderSQLConnSelect();
  const connSel = $("sqlConnSel");
  let conn = String(connId || (connSel && connSel.value) || "").trim();
  if (connSel && conn) connSel.value = conn;
  conn = String((connSel && connSel.value) || conn || "").trim();
  if (!conn) { toast(sqlT("sql.need_conn_run", "运行需要选择数据库连接"), "err"); return null; }
  const c = sqlConnById(conn);
  if (!c) { toast(sqlT("sql.conn_missing", "连接不存在或已删除，请刷新后重选"), "err"); return null; }
  if (c.enabled === false) { toast(sqlT("sql.conn_disabled", "连接已停用，请在「连接管理」中启用"), "err"); return null; }
  let schema = sqlActiveSchema() || c.database || "";
  if (!schema) schema = inferSchemaFromSQLClient(sql);
  if (!schema) {
    toast(sqlT("sql.schema_needed", "未指定数据库：请在上方 Database 下拉框选择库后再运行"), "err");
    const dbSel = $("sqlDbSel"); if (dbSel) dbSel.focus();
    return null;
  }
  setActiveSQLDatabase(schema);
  return { conn, schema };
}

async function runSQLQuery(connId, sqlOverride, opts) {
  if (connId && typeof connId === "object") { connId = null; sqlOverride = null; }
  opts = opts || {};
  const sql = (sqlOverride != null ? String(sqlOverride) : sqlText()).trim();
  if (!sql) { toast(sqlT("sql.empty", "请先输入 SQL"), "err"); return; }
  const target = await sqlResolveRunTarget(connId, sql);
  if (!target) return;

  const limit = sqlRunLimit();
  const offset = Math.max(0, parseInt(opts.offset || 0, 10) || 0);
  SQL_LAST_RUN = { conn: target.conn, schema: target.schema, sql, limit, offset };
  SQL_STREAM_ROWS = [];
  SQL_STREAM_COLS = null;

  const ac = new AbortController();
  SQL_RUN_ABORT = ac;
  const startedAt = Date.now();
  sqlSetRunning(true, startedAt);
  try {
    const r = await fetch(`${API}/sql/connections/${encodeURIComponent(target.conn)}/query/stream`, {
      method: "POST", headers: { "Content-Type": "application/json" }, signal: ac.signal,
      body: JSON.stringify({
        sql, schema: target.schema, database: target.schema,
        limit, offset, timeout_sec: sqlRunTimeout()
      })
    });
    if (!r.ok) {
      const j = await sqlReadErrorBody(r);
      toast(j.error, "err");
      renderSQLQueryError(j);
      return;
    }
    await sqlConsumeStream(r, { limit, offset });
  } catch (e) {
    if (e && e.name === "AbortError") {
      toast(sqlT("sql.run_cancelled", "已停止查询"), "info");
      renderSQLStreamResult({ cancelled: true, limit, offset });
    } else {
      const msg = sqlNetworkFailureMessage(e);
      toast(msg, "err");
      renderSQLQueryError({ error: msg });
    }
  } finally {
    sqlSetRunning(false);
    SQL_RUN_ABORT = null;
  }
}

/**
 * 失败必须说得出原因。
 *
 * 原来这里是 `await r.json().catch(()=>({}))` 然后 `j.error || "运行失败"`：只要响应体
 * 不是 JSON，用户看到的就是孤零零三个字「运行失败」——没有状态码、没有原文、没有下一步。
 * 而**恰恰是最常见的几种失败根本不返回 JSON**：反向代理超时/上游断开是 nginx 自己的
 * HTML 502/504 页；网关限流是纯文本；会话过期是 302 到登录页。于是"查询跑不了"这件事
 * 在用户那里永远是同一句废话，既没法自查也没法报障。
 *
 * 这里把三样东西都还给用户：状态码 + 服务端原文（哪怕不是 JSON）+ 针对该状态码的下一步动作。
 */
async function sqlReadErrorBody(r) {
  let raw = "";
  try { raw = await r.text(); } catch (_e) { raw = ""; }
  let parsed = null;
  try { parsed = raw ? JSON.parse(raw) : null; } catch (_e) { parsed = null; }
  if (parsed && typeof parsed === "object" && parsed.error) return parsed;

  // 不是 JSON：把 HTML 标签剥掉，只留正文，别把整页 nginx 错误页糊到面板上。
  const plain = String(raw || "").replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim().slice(0, 300);
  const hint = sqlHTTPStatusHint(r.status);
  const parts = [`HTTP ${r.status}${r.statusText ? " " + r.statusText : ""}`];
  if (hint) parts.push(hint);
  if (plain) parts.push(`${sqlT("sql.server_said", "服务端返回")}：${plain}`);
  // 版本不匹配的猜测要能被证实：顺手问一次服务端自己报的版本号，让用户拿去和镜像 tag 对。
  if (r.status === 404 || r.status === 405) {
    const ver = await sqlServerVersion();
    if (ver) parts.push(`${sqlT("sql.server_version", "服务端版本")}：${ver}`);
  }
  return Object.assign({}, parsed || {}, { error: parts.join(" · ") });
}

/** 取服务端自报的版本号，失败就返回空串——它只是用来佐证提示，不该让报错流程再抛一次。 */
async function sqlServerVersion() {
  try {
    const r = await fetch(`${API}/summary`, { headers: { Accept: "application/json" } });
    if (!r.ok) return "";
    const j = await r.json();
    return String((j && j.version) || "");
  } catch (_e) { return ""; }
}

/** 常见状态码 → 用户能照着做的下一步。措辞要指向动作，不要复述状态码含义。 */
function sqlHTTPStatusHint(status) {
  switch (status) {
    // 404/405 打在一个 POST 接口上，在本产品里几乎只有一个原因：**页面比服务端新**。
    // 经典版的 JS 是 go:embed 进二进制的，正常情况下页面和路由一定同版本；能对不上
    // 只有一种途径——Service Worker 缓存了新版页面，而容器里还是旧二进制
    // （compose 没真的换镜像、或 .env 钉了旧 tag）。
    // 旧到没有 JSON 兜底的二进制还会回一个纯文本 405（根路由 `GET /` 吞掉了 POST），
    // 那正是最让人摸不着头脑的一种：明明是"接口不存在"，却说成"方法不允许"。
    case 404:
    case 405:
      return sqlT("sql.err_stale_backend",
        "这个接口在正在运行的面板服务里不存在——页面比服务端新。多半是升级后容器没真正换镜像，或浏览器缓存了新版页面。请先强制刷新（Ctrl+F5）；仍然如此就说明服务端没升级上去。");
    case 401: return sqlT("sql.err_401", "登录状态已失效，请重新登录后再运行");
    case 403: return sqlT("sql.err_403", "当前角色无权执行该操作（导出需要操作员及以上）");
    case 413: return sqlT("sql.err_413", "SQL 文本过大，请拆分后再运行");
    case 429: return sqlT("sql.err_429", "请求过于频繁，请稍后重试");
    case 502: case 503:
      return sqlT("sql.err_502", "反向代理连不上面板服务，请检查服务是否在运行");
    case 504:
      return sqlT("sql.err_504", "反向代理等待超时——查询比代理的超时时间还长。请缩小查询范围，或调大 nginx 的 proxy_read_timeout");
  }
  if (status >= 500) return sqlT("sql.err_5xx", "服务端处理失败，详情见服务端日志");
  return "";
}

/** fetch 直接抛异常（连不上/被中断/证书问题）时的措辞。 */
function sqlNetworkFailureMessage(e) {
  const raw = String((e && e.message) || e || "");
  // 浏览器对这一类只给 "Failed to fetch"/"NetworkError"，原文本身没有任何信息量，
  // 必须由我们补上"可能是什么、该查哪里"。
  if (/failed to fetch|networkerror|load failed|connection closed/i.test(raw)) {
    return sqlT("sql.err_network",
      "与服务端的连接中断：可能是查询时间超过了反向代理的超时上限，或网络/服务端中途断开。请缩小查询范围（加 WHERE、降低行数上限）后重试。");
  }
  return raw || sqlT("sql.run_failed", "运行失败");
}

/** 逐行读取 NDJSON：表头先出来，行数据边到边画。 */
async function sqlConsumeStream(resp, ctx) {
  const reader = resp.body && resp.body.getReader ? resp.body.getReader() : null;
  if (!reader) { // 极老内核没有流式读取：退回一次性读取，功能不打折，只是没有"边到边画"
    const text = await resp.text();
    // 单行解析失败不该让整次查询颗粒无收：能画多少画多少，最后由 end 标记是否完整。
    text.split("\n").forEach(line => {
      if (!line.trim()) return;
      try { sqlHandleStreamLine(JSON.parse(line), ctx); } catch (_e) { /* 半行/脏行：跳过 */ }
    });
    sqlFinishStream(ctx);
    return;
  }
  const dec = new TextDecoder();
  let buf = "";
  let lastPaint = 0;
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let nl;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line) continue;
      let msg = null;
      try { msg = JSON.parse(line); } catch (e) { continue; }
      sqlHandleStreamLine(msg, ctx);
    }
    // 边到边画，但别每批都重排整张表——20fps 足够"看起来在动"。
    if (Date.now() - lastPaint > 250) {
      lastPaint = Date.now();
      renderSQLStreamResult(ctx, true);
    }
  }
  // 收尾：最后一行可能没带换行符（服务端被掐断时尤其如此），别把它丢掉。
  const tail = buf.trim();
  if (tail) { try { sqlHandleStreamLine(JSON.parse(tail), ctx); } catch (_e) { /* 半行 JSON，丢弃 */ } }
  sqlFinishStream(ctx);
}

/**
 * 流走完了，但**完整的流一定以 type:"end" 收尾**。没收到 end 就意味着连接是被掐断的：
 * 反向代理超时、服务端重启、中间网络断开都会这样。
 *
 * 这里原本直接照常渲染——于是「查到一半被切断的 3000 行」和「真的只有 3000 行」在页面上
 * 长得一模一样。SQL 工具里这是最不能接受的一类错误：用户会拿着残缺的结果去做判断，
 * 而且完全不知道它是残缺的。所以宁可显眼地标出来。
 */
function sqlFinishStream(ctx) {
  if (ctx && !ctx.end) {
    ctx.incomplete = true;
    toast(sqlT("sql.stream_truncated",
      "结果不完整：数据还没传完连接就断了（常见于反向代理超时）。请缩小查询范围后重试，不要直接使用这批数据。"), "err");
  }
  renderSQLStreamResult(ctx);
}

function sqlHandleStreamLine(msg, ctx) {
  if (!msg || typeof msg !== "object") return;
  if (msg.type === "meta") {
    SQL_STREAM_COLS = Array.isArray(msg.columns) ? msg.columns : [];
    ctx.meta = msg;
    renderSQLStreamResult(ctx, true);
    return;
  }
  if (msg.type === "rows" && Array.isArray(msg.rows)) {
    for (const row of msg.rows) SQL_STREAM_ROWS.push(row);
    return;
  }
  if (msg.type === "end") {
    ctx.end = msg;
    if (msg.error) toast(msg.error, "err");
    return;
  }
  if (msg.type === "error") {
    ctx.end = msg;
    toast(msg.error || sqlT("sql.run_failed", "运行失败"), "err");
  }
}

window.runSQLQuery = runSQLQuery;

function renderSQLQueryError(j) {
  const box = $("sqlResultPanel");
  if (!box) return;
  const timing = [];
  if (j.exec_ms != null) timing.push(`执行 ${j.exec_ms} ms`);
  if (j.fetch_ms != null) timing.push(`返回 ${j.fetch_ms} ms`);
  if (j.total_ms != null) timing.push(`合计 ${j.total_ms} ms`);
  box.innerHTML = `
    <div class="hint sql-trunc-warn" style="display:block;margin:0 0 10px">${esc(j.error || sqlT("sql.run_failed", "运行失败"))}</div>
    ${timing.length ? `<div class="sql-query-meta">${esc(timing.join(" · "))}</div>` : ""}
    <div class="hint" style="margin-top:8px">${esc(sqlT("sql.run_hint", "仅允许只读 SELECT/WITH/SHOW；写操作请走变更单。"))}</div>`;
}

// 一次最多往 DOM 里放多少行。浏览器渲染几万行 <tr> 会直接卡死（这正是"数据量大就卡"
// 的客户端那一半）；服务端可以给到 5 万行，但页面上先画 500 行，其余按需展开。
const SQL_DOM_ROW_CHUNK = 500;

/** renderSQLStreamResult 画流式结果。partial=true 表示还在流中（不画统计尾巴）。 */
function renderSQLStreamResult(ctx, partial) {
  const box = $("sqlResultPanel");
  if (!box) return;
  SQL_LAST_RENDER_CTX = ctx || {};
  const cols = SQL_STREAM_COLS || [];
  const rows = SQL_STREAM_ROWS || [];
  const meta = (ctx && ctx.meta) || {};
  const end = (ctx && ctx.end) || {};
  const shown = Math.min(rows.length, box._sqlShown || SQL_DOM_ROW_CHUNK);
  box._sqlShown = shown;

  const bits = [];
  if (meta.schema) bits.push(`<span class="badge">${esc(meta.schema)}</span>`);
  bits.push(`<span><b>${rows.length}</b> ${esc(sqlT("sql.query_rows", "行"))}</span>`);
  if (end.exec_ms != null || meta.exec_ms != null) {
    bits.push(`<span>${esc(sqlT("sql.exec_ms", "执行时长"))} <b>${end.exec_ms ?? meta.exec_ms}</b> ms</span>`);
  }
  if (end.fetch_ms != null) bits.push(`<span>${esc(sqlT("sql.fetch_ms", "数据返回"))} <b>${end.fetch_ms}</b> ms</span>`);
  if (end.total_ms != null) bits.push(`<span>${esc(sqlT("sql.total_ms", "合计"))} <b>${end.total_ms}</b> ms</span>`);
  if (ctx && ctx.offset) bits.push(`<span class="badge">OFFSET ${ctx.offset}</span>`);
  if (end.truncated) bits.push(`<span class="badge warn">${esc(sqlT("sql.query_truncated", "已截断"))} ≤${ctx.limit}</span>`);
  if (ctx && ctx.cancelled) bits.push(`<span class="badge warn">${esc(sqlT("sql.run_cancelled", "已停止查询"))}</span>`);
  if (ctx && ctx.incomplete) bits.push(`<span class="badge err">${esc(sqlT("sql.stream_incomplete", "结果不完整"))}</span>`);
  if (partial) bits.push(`<span class="badge">${esc(sqlT("sql.streaming", "接收中…"))}</span>`);

  // 结果不完整时不给导出/翻页：把残缺数据导成 CSV 发出去，比查询失败本身危害更大。
  const actions = (partial || (ctx && ctx.incomplete)) ? "" : `
    <div class="sql-result-actions">
      <button type="button" class="btn sm" data-sql-page="prev"${(ctx.offset || 0) <= 0 ? " disabled" : ""}>${esc(sqlT("sql.page_prev", "上一页"))}</button>
      <button type="button" class="btn sm" data-sql-page="next"${end.truncated ? "" : " disabled"}>${esc(sqlT("sql.page_next", "下一页"))}</button>
      <button type="button" class="btn sm" data-sql-copy="csv">${esc(sqlT("sql.copy_csv", "复制为 CSV"))}</button>
      <button type="button" class="btn sm" data-sql-export="csv">${esc(sqlT("sql.export_csv", "导出 CSV"))}</button>
    </div>`;

  if (!cols.length) {
    box.innerHTML = `<div class="sql-query-meta">${bits.join("")}</div>` +
      `<div class="hint">${esc(sqlT("sql.query_empty", "查询成功，无返回列（或无结果集）"))}</div>` + actions;
    return;
  }
  const head = cols.map(c => `<th>${esc(c)}</th>`).join("");
  const body = rows.slice(0, shown).map(row => {
    const cells = cols.map((c, i) => {
      const v = Array.isArray(row) ? row[i] : (row ? row[c] : null);
      const text = v == null ? "NULL" : String(v);
      const cls = v == null ? " class=\"sql-null\"" : (typeof v === "number" ? " class=\"num\"" : "");
      return `<td${cls} title="${esc(text.length > 500 ? text.slice(0, 500) + "…" : text)}">${esc(text.length > 200 ? text.slice(0, 200) + "…" : text)}</td>`;
    }).join("");
    return `<tr>${cells}</tr>`;
  }).join("");
  const more = rows.length > shown
    ? `<div class="sql-query-hint"><button type="button" class="btn sm" data-sql-more="1">${esc(sqlT("sql.show_more", "再显示 500 行"))}</button> ${esc(sqlT("sql.dom_capped", "（页面只渲染部分行以保持流畅，导出/复制不受影响）"))}</div>`
    : "";
  box.innerHTML = `<div class="sql-query-meta">${bits.join("")}</div>${actions}
    <div class="sql-query-wrap"><table class="sql-query-table">
      <thead><tr>${head}</tr></thead>
      <tbody>${body || `<tr><td colspan="${cols.length}">${esc(sqlT("sql.query_no_rows", "无数据行"))}</td></tr>`}</tbody>
    </table></div>${more}`;
}

/** 结果区的按钮：翻页 / 展开更多 / 复制 / 导出。 */
function onSQLResultAction(e) {
  const box = $("sqlResultPanel");
  const more = e.target.closest && e.target.closest("[data-sql-more]");
  if (more && box) {
    box._sqlShown = (box._sqlShown || SQL_DOM_ROW_CHUNK) + SQL_DOM_ROW_CHUNK;
    renderSQLStreamResult(SQL_LAST_RENDER_CTX || {});
    return true;
  }
  const page = e.target.closest && e.target.closest("[data-sql-page]");
  if (page && SQL_LAST_RUN) {
    const dir = page.dataset.sqlPage;
    const step = SQL_LAST_RUN.limit || 1000;
    const next = dir === "next" ? (SQL_LAST_RUN.offset || 0) + step : Math.max(0, (SQL_LAST_RUN.offset || 0) - step);
    runSQLQuery(SQL_LAST_RUN.conn, SQL_LAST_RUN.sql, { offset: next });
    return true;
  }
  const copy = e.target.closest && e.target.closest("[data-sql-copy]");
  if (copy) {
    const text = sqlRowsToCSV(SQL_STREAM_COLS || [], SQL_STREAM_ROWS || []);
    if (!text) { toast(sqlT("sql.nothing_to_copy", "没有可复制的结果"), "info"); return true; }
    copyToClipboardOrPrompt(text).then(ok => { if (ok) toast(I18N.t("toast.copied", "已复制"), "ok"); });
    return true;
  }
  const exp = e.target.closest && e.target.closest("[data-sql-export]");
  if (exp) { exportSQLResultCSV(); return true; }
  return false;
}

/**
 * 把列式结果拼成 CSV（客户端复制用；导出走服务端流式接口，不受行数上限影响）。
 *
 * 单元格内容是业务库里的任意文本。粘进 Excel/WPS 时，`=`/`+`/`-`/`@` 开头的格子
 * 同样按公式求值——粘贴路径和下载路径在这一点上没有区别，所以判据与 export.js 的
 * expCsvNeutralize、服务端 neutralizeCSVFormula 完全一致（纯数字放行）。
 */
function sqlRowsToCSV(cols, rows) {
  if (!cols.length) return "";
  const cell = (v) => {
    if (v == null) return "";
    const s = expCsvNeutralize(String(v));
    return /[",\n\r]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
  };
  const lines = [cols.map(cell).join(",")];
  for (const row of rows) {
    lines.push(cols.map((c, i) => cell(Array.isArray(row) ? row[i] : (row ? row[c] : null))).join(","));
  }
  return lines.join("\n");
}

/**
 * exportSQLResultCSV 走服务端流式导出：**重跑一次查询直接写 CSV**，不受页面行数上限影响。
 * 页面上只画了 500 行、内存里只有 5 万行，但导出可以拿到几十万行——这正是"数据量大"时
 * 用户真正需要的出口。
 */
async function exportSQLResultCSV() {
  if (!SQL_LAST_RUN) { toast(sqlT("sql.run_first", "请先运行一次查询"), "info"); return; }
  const btnLabel = sqlT("sql.exporting", "正在导出…");
  toast(btnLabel, "info");
  try {
    const r = await fetch(`${API}/sql/connections/${encodeURIComponent(SQL_LAST_RUN.conn)}/query/export`, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        sql: SQL_LAST_RUN.sql, schema: SQL_LAST_RUN.schema, database: SQL_LAST_RUN.schema,
        offset: SQL_LAST_RUN.offset || 0, timeout_sec: 60,
        filename: "sql-result-" + new Date().toISOString().slice(0, 19).replace(/[:T]/g, "")
      })
    });
    if (!r.ok) {
      const j = await r.json().catch(() => ({}));
      toast(j.error || sqlT("sql.export_failed", "导出失败"), "err");
      return;
    }
    const blob = await r.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = (r.headers.get("Content-Disposition") || "").split("filename=")[1]?.replace(/"/g, "") || "sql-result.csv";
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 5000);
    toast(sqlT("sql.export_ok", "导出完成"), "ok");
  } catch (e) {
    toast(String(e && e.message || e), "err");
  }
}

let SQL_LAST_RENDER_CTX = null;

function renderSQLQueryResult(j) {
  const box = $("sqlResultPanel");
  if (!box) return;
  const cols = Array.isArray(j.columns) ? j.columns : [];
  const rows = Array.isArray(j.rows) ? j.rows : [];
  const trunc = j.truncated
    ? `<span class="badge warn">${esc(sqlT("sql.query_truncated", "已截断"))} ≤${j.limit || 200}</span>`
    : "";
  const schema = j.schema ? `<span class="badge">${esc(j.schema)}</span>` : "";
  const meta = `
    <div class="sql-query-meta">
      ${schema}
      <span><b>${rows.length}</b> ${esc(sqlT("sql.query_rows", "行"))}</span>
      <span>${esc(sqlT("sql.exec_ms", "执行时长"))} <b>${j.exec_ms != null ? j.exec_ms : "—"}</b> ms</span>
      <span>${esc(sqlT("sql.fetch_ms", "数据返回"))} <b>${j.fetch_ms != null ? j.fetch_ms : "—"}</b> ms</span>
      <span>${esc(sqlT("sql.total_ms", "合计"))} <b>${j.total_ms != null ? j.total_ms : "—"}</b> ms</span>
      ${trunc}
    </div>`;
  if (!cols.length) {
    box.innerHTML = meta + `<div class="hint">${esc(sqlT("sql.query_empty", "查询成功，无返回列（或无结果集）"))}</div>`;
    return;
  }
  const head = cols.map(c => `<th>${esc(c)}</th>`).join("");
  const body = rows.map(row => {
    const cells = cols.map(c => {
      const v = row && Object.prototype.hasOwnProperty.call(row, c) ? row[c] : null;
      const text = v == null ? "NULL" : String(v);
      const cls = v == null ? " class=\"sql-null\"" : (typeof v === "number" ? " class=\"num\"" : "");
      return `<td${cls} title="${esc(text)}">${esc(text.length > 200 ? text.slice(0, 200) + "…" : text)}</td>`;
    }).join("");
    return `<tr>${cells}</tr>`;
  }).join("");
  box.innerHTML = `${meta}
    <div class="sql-query-wrap"><table class="sql-query-table">
      <thead><tr>${head}</tr></thead>
      <tbody>${body || `<tr><td colspan="${cols.length}">${esc(sqlT("sql.query_no_rows", "无数据行"))}</td></tr>`}</tbody>
    </table></div>
    ${cols.length > 6 ? `<div class="sql-query-hint">${esc(sqlT("sql.query_scroll_hint", "字段较多时可左右滚动查看；单元格悬停可看全文"))}</div>` : ""}`;
}

async function runSQLExplain(connId, sqlOverride) {
  // 点击处理器会把 Event 当作第一参数传入，必须忽略
  if (connId && typeof connId === "object") { connId = null; sqlOverride = null; }
  const sql = (sqlOverride != null ? String(sqlOverride) : sqlText()).trim();
  let conn = String(connId || ($("sqlConnSel") && $("sqlConnSel").value)
    || ($("sqlSlowConnSel") && $("sqlSlowConnSel").value)
    || (SQL_SLOW_REPORT && SQL_SLOW_REPORT.connection_id) || "").trim();
  if (!sql) { toast(sqlT("sql.empty", "请先输入 SQL"), "err"); return; }
  await loadSQLConnections();
  renderSQLConnSelect();
  const connSel = $("sqlConnSel");
  if (connSel && conn) connSel.value = conn;
  conn = String((connSel && connSel.value) || conn || "").trim();
  if (!conn) { toast(sqlT("sql.need_conn", "EXPLAIN 需要选择数据库连接"), "err"); return; }
  const c = sqlConnById(conn);
  if (!c) { toast(sqlT("sql.conn_missing", "连接不存在或已删除，请刷新后重选"), "err"); return; }
  if (c.enabled === false) { toast(sqlT("sql.conn_disabled", "连接已停用，请在「连接管理」中启用"), "err"); return; }
  // Prefer SELECT/WITH for re-EXPLAIN after DDL (index DDL itself is not EXPLAIN-able).
  const explainSQL = /^\s*(create|alter)\b/i.test(sql) ? sqlText().trim() || sql : sql;
  let schema = sqlActiveSchema() || c.database || "";
  if (!schema) schema = inferSchemaFromSQLClient(explainSQL);
  if (!schema) {
    toast(sqlT("sql.schema_needed", "未指定数据库：请在上方 Database 下拉框选择库后再 EXPLAIN"), "err");
    const dbSel = $("sqlDbSel"); if (dbSel) dbSel.focus();
    return;
  }
  setActiveSQLDatabase(schema);
  await withLoading("sqlExplainBtn", async () => {
    try {
      const payload = { sql: explainSQL, schema, database: schema };
      const r = await fetch(`${API}/sql/connections/${encodeURIComponent(conn)}/explain`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      });
      const j = await r.json().catch(() => ({}));
      if (!r.ok) {
        toast(j.error || "EXPLAIN 失败", "err");
        if (j.prepared_sql || (Array.isArray(j.prepare_notes) && j.prepare_notes.length)) {
          renderSQLExplainError(j);
        }
        return;
      }
      SQL_LAST.explain = j;
      renderSQLExplain(j);
    } catch (e) { toast(String(e), "err"); }
  });
}
window.runSQLExplain = runSQLExplain;

function renderSQLFindings(j) {
  const box = $("sqlResultPanel");
  if (!box) return;
  const findings = Array.isArray(j.findings) ? j.findings : [];
  const score = typeof j.score === "number" ? j.score : "-";
  const rows = findings.map(f => {
    const lv = f.level || "info";
    return `<div class="sql-finding ${esc(lv)}">
      <div class="sql-finding-head"><span class="sql-lv">${esc(lv)}</span><strong>${esc(f.title || f.id)}</strong><code class="mono">${esc(f.id || "")}</code></div>
      <div class="sql-finding-detail">${esc(f.detail || "")}</div>
      ${f.suggest ? `<div class="sql-finding-suggest">${esc(f.suggest)}</div>` : ""}
    </div>`;
  }).join("") || `<div class="hint">${esc(sqlT("sql.no_findings", "未发现问题"))}</div>`;
  box.innerHTML = `<div class="sql-score">${esc(sqlT("sql.score", "审核分"))}: <b>${score}</b></div>${rows}`;
}

function renderSQLOptimize(j) {
  const box = $("sqlResultPanel");
  if (!box) return;
  const sug = (Array.isArray(j.suggestions) ? j.suggestions : []).map(s =>
    `<li><strong>${esc(s.title || s.id || "")}</strong> — ${esc(s.detail || s.message || "")}${s.sql ? `<pre class="mono sql-snippet">${esc(s.sql)}</pre>` : ""}</li>`
  ).join("");
  const idx = (Array.isArray(j.index_hints) ? j.index_hints : []).map(h =>
    `<li>${esc(typeof h === "string" ? h : (h.hint || h.sql || JSON.stringify(h)))}</li>`
  ).join("");
  const rewritten = j.rewritten_sql || "";
  box.innerHTML = `
    <div class="sql-opt-block">
      <div class="sql-opt-head">
        <span>${esc(sqlT("sql.rewritten", "改写建议"))}</span>
        <button type="button" class="btn sm" id="sqlCopyRewritten">${esc(sqlT("sql.copy", "复制"))}</button>
        <button type="button" class="btn sm" id="sqlApplyRewritten">${esc(sqlT("sql.apply", "应用到编辑器"))}</button>
      </div>
      <pre class="mono sql-rewritten" id="sqlRewrittenBody">${esc(rewritten || sqlT("sql.no_rewrite", "（无静态改写，见下方建议）"))}</pre>
    </div>
    <div class="sql-opt-block"><div class="sql-opt-head">${esc(sqlT("sql.suggestions", "优化建议"))}</div><ul class="sql-ul">${sug || "<li>—</li>"}</ul></div>
    <div class="sql-opt-block"><div class="sql-opt-head">${esc(sqlT("sql.index_hints", "索引提示"))}</div><ul class="sql-ul">${idx || "<li>—</li>"}</ul></div>`;
  const copyBtn = $("sqlCopyRewritten");
  if (copyBtn) copyBtn.onclick = () => {
    if (!rewritten) return;
    navigator.clipboard.writeText(rewritten).then(() => toast(sqlT("sql.copied", "已复制"), "ok")).catch(() => toast("复制失败", "err"));
  };
  const applyBtn = $("sqlApplyRewritten");
  if (applyBtn) applyBtn.onclick = () => { if (rewritten) { setSQLText(rewritten); toast(sqlT("sql.applied", "已应用"), "ok"); } };
}

function renderSQLExplain(j) {
  const box = $("sqlResultPanel");
  if (!box) return;
  const a = j.analysis || {};
  const detail = j.detail || {};
  const hits = Array.isArray(a.table_access) ? a.table_access : [];
  const rows = hits.map(h => `<tr>
    <td>${esc(h.table || "")}</td>
    <td>${esc(h.access_type || "")}</td>
    <td>${esc(h.key || "—")}</td>
    <td>${esc(h.possible_keys || "—")}</td>
    <td>${esc(String(h.rows != null ? h.rows : ""))}</td>
    <td>${esc(String(h.filtered != null ? h.filtered : ""))}</td>
    <td>${h.full_scan_risk ? "⚠" : (h.key ? "✓" : "")}</td>
  </tr>`).join("");
  const planBlock = j.plan
    ? `<details class="sql-raw" open><summary>PostgreSQL TEXT Plan</summary><pre class="mono">${esc(j.plan)}</pre></details>`
    : "";
  const jsonRaw = typeof j.raw === "string" ? j.raw : JSON.stringify(j.explain_json, null, 2);

  const health = detail.health || "";
  const healthLabel = health === "good" ? "较好" : (health === "poor" ? "较差" : (health === "caution" ? "需关注" : ""));
  const healthBadge = health
    ? `<span class="badge ${health === "good" ? "ok" : (health === "poor" ? "crit" : "warn")}">${esc(sqlT("sql.explain_health", "计划健康"))} · ${esc(healthLabel)}</span>`
    : "";

  const steps = Array.isArray(detail.steps) ? detail.steps : [];
  const stepsHTML = steps.length
    ? `<div class="sql-explain-steps">${steps.map((s, i) => `
        <div class="sql-explain-step ${esc(s.severity || "info")}">
          <div class="sql-explain-step-head">
            <span class="sql-explain-step-n">${i + 1}</span>
            <strong>${esc(s.verdict || s.access_type || "")}</strong>
            ${s.table ? `<code class="mono">${esc(s.table)}</code>` : ""}
            ${s.access_type ? `<span class="tag">${esc(s.access_type)}</span>` : ""}
            ${s.rows != null && s.rows !== "" ? `<span class="tag">rows≈${esc(String(s.rows))}</span>` : ""}
          </div>
          <div class="sql-explain-step-body">${esc(s.analysis || "")}</div>
          ${s.condition ? `<div class="sql-explain-step-cond"><span class="muted">条件</span> <code class="mono">${esc(s.condition)}</code></div>` : ""}
          ${s.suggest ? `<div class="sql-finding-suggest">${esc(s.suggest)}</div>` : ""}
        </div>`).join("")}</div>`
    : `<div class="hint">${esc(sqlT("sql.no_explain_steps", "暂无逐步解析（无表访问节点）"))}</div>`;

  const findings = Array.isArray(detail.findings) ? detail.findings
    : (Array.isArray(j.findings) ? j.findings : []);
  const findingsHTML = findings.map(f => {
    const lv = f.level || "info";
    return `<div class="sql-finding ${esc(lv)}">
      <div class="sql-finding-head"><span class="sql-lv">${esc(lv)}</span><strong>${esc(f.title || f.id || "")}</strong></div>
      <div class="sql-finding-detail">${esc(f.detail || "")}</div>
      ${f.suggest ? `<div class="sql-finding-suggest">${esc(f.suggest)}</div>` : ""}
    </div>`;
  }).join("") || `<div class="hint">${esc(sqlT("sql.no_findings", "未发现问题"))}</div>`;

  const hints = Array.isArray(detail.index_hints) ? detail.index_hints
    : (Array.isArray(j.index_hints) ? j.index_hints : []);
  const hintsHTML = hints.length
    ? hints.map((h, i) => {
        const cols = Array.isArray(h.columns) ? h.columns.join(", ") : "";
        const ddl = h.ddl || "";
        const id = `sqlExplainDdl_${i}`;
        return `<div class="sql-index-card">
          <div class="sql-index-card-head">
            <strong>${esc(h.table || sqlT("sql.unknown_table", "表"))}</strong>
            ${cols ? `<code class="mono">(${esc(cols)})</code>` : ""}
            ${h.meta ? `<span class="tag">meta</span>` : ""}
          </div>
          <div class="sql-finding-detail">${esc(h.reason || "")}</div>
          ${ddl ? `<pre class="mono sql-snippet" id="${id}">${esc(ddl)}</pre>
            <div class="sql-index-card-acts">
              <button type="button" class="btn sm" data-copy-ddl="${id}">${esc(sqlT("sql.copy", "复制"))}</button>
              <button type="button" class="btn sm" data-apply-ddl="${id}">${esc(sqlT("sql.apply", "应用到编辑器"))}</button>
            </div>` : ""}
        </div>`;
      }).join("")
    : `<div class="hint">${esc(sqlT("sql.no_index_hint", "暂无明确索引建议（可能已有合适索引，或无法从条件推导列）"))}</div>`;

  const suggestions = Array.isArray(detail.suggestions) ? detail.suggestions
    : (Array.isArray(j.suggestions) ? j.suggestions : []);
  const sugHTML = suggestions.map(s => `
    <div class="sql-finding ${esc(s.level || "info")}">
      <div class="sql-finding-head"><span class="sql-lv">${esc(s.level || "info")}</span><strong>${esc(s.title || s.id || "")}</strong></div>
      <div class="sql-finding-detail">${esc(s.detail || "")}</div>
      ${s.suggest ? `<div class="sql-finding-suggest">${esc(s.suggest)}</div>` : ""}
    </div>`).join("") || `<div class="hint">—</div>`;

  const overview = detail.overview || a.summary || (j.driver === "postgres" ? "PostgreSQL EXPLAIN" : "");

  box.innerHTML = `
    ${Array.isArray(j.prepare_notes) && j.prepare_notes.length
      ? `<div class="hint sql-prepare-notes">${esc(j.prepare_notes.join(" · "))}</div>` : ""}
    <div class="sql-explain-summary">${esc(a.summary || "")} ${healthBadge}</div>
    <div class="table-wrap"><table class="data sql-explain-table">
      <thead><tr><th>table</th><th>type</th><th>key</th><th>possible_keys</th><th>rows</th><th>filtered</th><th></th></tr></thead>
      <tbody>${rows || `<tr><td colspan="7">${esc(j.plan ? "见下方 TEXT Plan" : sqlT("sql.no_explain_rows", "无表访问节点"))}</td></tr>`}</tbody>
    </table></div>
    ${planBlock}
    ${j.prepared_sql ? `<details class="sql-raw"><summary>${esc(sqlT("sql.prepared_sql", "实际 EXPLAIN 语句"))}</summary><pre class="mono">${esc(j.prepared_sql)}</pre></details>` : ""}
    <details class="sql-raw"><summary>EXPLAIN JSON</summary><pre class="mono">${esc(jsonRaw || "")}</pre></details>

    <div class="sql-opt-block sql-explain-detail">
      <div class="sql-opt-head">${esc(sqlT("sql.explain_detail", "执行计划详细分析"))}${detail.metadata_used ? '<span class="tag">meta</span>' : ""}</div>
      <div class="sql-explain-overview">${esc(overview)}</div>
      ${stepsHTML}
    </div>
    <div class="sql-opt-block">
      <div class="sql-opt-head">${esc(sqlT("sql.explain_findings", "风险与问题"))}</div>
      ${findingsHTML}
    </div>
    <div class="sql-opt-block">
      <div class="sql-opt-head">${esc(sqlT("sql.index_hints", "索引建议"))}</div>
      ${hintsHTML}
    </div>
    <div class="sql-opt-block">
      <div class="sql-opt-head">${esc(sqlT("sql.suggestions", "优化建议"))}</div>
      ${sugHTML}
    </div>`;

  box.querySelectorAll("[data-copy-ddl]").forEach(btn => {
    btn.addEventListener("click", () => {
      const el = $(btn.getAttribute("data-copy-ddl"));
      const text = el ? el.textContent : "";
      if (!text) return;
      navigator.clipboard.writeText(text).then(() => toast(sqlT("sql.copied", "已复制"), "ok")).catch(() => toast("复制失败", "err"));
    });
  });
  box.querySelectorAll("[data-apply-ddl]").forEach(btn => {
    btn.addEventListener("click", () => {
      const el = $(btn.getAttribute("data-apply-ddl"));
      const text = el ? el.textContent : "";
      if (!text) return;
      setSQLText(text);
      toast(sqlT("sql.applied", "已应用"), "ok");
    });
  });
}

function renderSQLExplainError(j) {
  const box = $("sqlResultPanel");
  if (!box) return;
  const notes = Array.isArray(j.prepare_notes) ? j.prepare_notes.join(" · ") : "";
  box.innerHTML = `
    <div class="hint sql-trunc-warn" style="display:block;margin:0 0 10px">${esc(j.error || sqlT("sql.explain_failed", "EXPLAIN 失败"))}</div>
    ${notes ? `<div class="hint sql-prepare-notes">${esc(notes)}</div>` : ""}
    ${j.prepared_sql ? `<details class="sql-raw" open><summary>${esc(sqlT("sql.prepared_sql", "实际 EXPLAIN 语句"))}</summary><pre class="mono">${esc(j.prepared_sql)}</pre></details>
      <div class="hint" style="margin-top:8px">${esc(sqlT("sql.explain_fail_hint", "上方为发送给数据库的规范化语句。请核对函数名/占位符探测值，或粘贴可执行的完整 SQL 后重试。"))}</div>` : ""}`;
}

function sqlAssistContext() {
  const parts = [
    `方言: ${sqlDialect()}`,
    `SQL:\n${sqlText().trim()}`
  ];
  if (SQL_LAST.analyze) {
    parts.push(`全面分析: ${JSON.stringify({
      score: SQL_LAST.analyze.score,
      score_breakdown: SQL_LAST.analyze.score_breakdown,
      findings: SQL_LAST.analyze.findings,
      index_hints: SQL_LAST.analyze.index_hints,
      explain: SQL_LAST.analyze.explain,
      metadata_used: SQL_LAST.analyze.metadata_used
    }).slice(0, 8000)}`);
  } else {
    if (SQL_LAST.audit) {
      parts.push(`审核分: ${SQL_LAST.audit.score}\nFindings: ${JSON.stringify(SQL_LAST.audit.findings || []).slice(0, 4000)}`);
    }
    if (SQL_LAST.optimize) {
      parts.push(`静态优化: ${JSON.stringify({
        rewritten_sql: SQL_LAST.optimize.rewritten_sql,
        suggestions: SQL_LAST.optimize.suggestions,
        index_hints: SQL_LAST.optimize.index_hints
      }).slice(0, 4000)}`);
    }
      if (SQL_LAST.explain) {
      parts.push(`EXPLAIN: ${JSON.stringify({
        analysis: SQL_LAST.explain.analysis,
        plan: SQL_LAST.explain.plan,
        driver: SQL_LAST.explain.driver
      }).slice(0, 4000)}`);
    }
  }
  return parts.join("\n\n");
}

function openSQLAI(task) {
  if (typeof openAIAssist !== "function") {
    toast(sqlT("assist.unavailable", "AI 面板未就绪"), "err");
    return;
  }
  const titles = {
    sql_beautify: sqlT("sql.ai_beautify", "AI · SQL 美化"),
    sql_audit: sqlT("sql.ai_audit", "AI · SQL 深度审核"),
    sql_optimize: sqlT("sql.ai_optimize", "AI · SQL 深度优化"),
    sql_remediation: sqlT("sql.ai_remediation", "AI · SQL 优化闭环")
  };
  const connId = $("sqlConnSel") && $("sqlConnSel").value;
  const isPlan = task === "sql_remediation";
  openAIAssist({
    task,
    title: titles[task] || "AI · SQL",
    mode: task === "sql_beautify" ? "generate" : "analyze",
    context: sqlAssistContext() + (connId ? `\n\nconnection_id=${connId}` : ""),
    applyLabel: isPlan ? sqlT("ai.apply_actions", "应用建议动作") : sqlT("sql.apply", "应用到编辑器"),
    applyTo: async (code) => {
      if (isPlan && typeof window.applyOpsActionPlan === "function") {
        const plan = typeof window.parseOpsActionPlan === "function" ? window.parseOpsActionPlan(code) : null;
        if (plan && Array.isArray(plan.actions) && plan.actions.length) {
          return window.applyOpsActionPlan(plan, {
            source: "sql",
            connectionId: connId,
            selectSQL: setSQLText,
            reExplainSQL: sqlText(),
            refresh: async () => { await runSQLAnalyze(); },
            onDDLResult: (res) => { if (typeof showSQLExplainDiffResult === "function") showSQLExplainDiffResult(res); },
          });
        }
      }
      if (code) { setSQLText(code); toast(sqlT("sql.applied", "已应用"), "ok"); }
      if (/^\s*(select|with)\b/i.test(code || "")) SQL_VERIFY_SQL = code;
      return true;
    }
  });
}

function renderSQLConnList() {
  const list = $("sqlConnList");
  if (!list) return;
  if (!SQL_CONNS.length) {
    list.innerHTML = `<div class="ds-empty"><span class="ds-empty-icon">🗄</span>${esc(sqlT("sql.conn_empty", "还没有 MySQL 连接。管理员可添加只读账号用于 EXPLAIN。"))}</div>`;
    return;
  }
  list.innerHTML = SQL_CONNS.map(c => {
    const status = c.enabled !== false
      ? '<span class="ds-status on"><span class="ds-status-dot"></span>启用</span>'
      : '<span class="ds-status off"><span class="ds-status-dot"></span>停用</span>';
    return `<div class="ds-card prom${c.enabled === false ? " ds-off" : ""}" data-id="${esc(c.id)}">
      <div class="ds-type-icon prom">SQL</div>
      <div class="ds-info">
        <div class="ds-name">${esc(c.name)}</div>
        <div class="ds-url"><span>${esc(c.user || "")}@${esc(c.host)}:${c.port || ((c.driver === "postgres") ? 5432 : 3306)}${c.database ? "/" + esc(c.database) : ""}</span>
          <span class="ds-auth">${esc(c.driver || "mysql")} · ${esc(c.env || "prod")} · ${esc(c.version_hint || "auto")}${c.slow_sql && c.slow_sql.enabled ? " · 慢SQL" : ""}</span></div>
      </div>
      ${status}
      <div class="ds-actions admin-only">
        <button class="btn sm" data-sqlconn="test" data-id="${esc(c.id)}">${esc(sqlT("sql.test", "测试"))}</button>
        <button class="btn sm" data-sqlconn="edit" data-id="${esc(c.id)}">${esc(sqlT("ui.edit", "编辑"))}</button>
        <button class="btn danger sm" data-sqlconn="del" data-id="${esc(c.id)}">${esc(sqlT("ui.delete", "删除"))}</button>
      </div>
    </div>`;
  }).join("");
}

function openSQLConnModal(c) {
  $("sqlConnModalTitle").textContent = c ? sqlT("sql.edit_conn", "编辑连接") : sqlT("sql.add_conn", "添加连接");
  $("sqlConnId").value = c ? c.id : "";
  $("sqlConnName").value = c ? (c.name || "") : "";
  $("sqlConnEnv").value = c ? (c.env || "prod") : "prod";
  const drv = $("sqlConnDriver");
  if (drv) drv.value = c ? (c.driver || "mysql") : "mysql";
  $("sqlConnHost").value = c ? (c.host || "") : "";
  const defaultPort = (c && c.driver === "postgres") || (drv && drv.value === "postgres") ? 5432 : 3306;
  $("sqlConnPort").value = c ? (c.port || defaultPort) : defaultPort;
  syncSQLConnDriverUI();
  $("sqlConnUser").value = c ? (c.user || "") : "";
  $("sqlConnPass").value = c ? (c.password || "") : "";
  $("sqlConnDB").value = c ? (c.database || "") : "";
  $("sqlConnTLS").value = c ? (c.tls || "") : "";
  $("sqlConnParams").value = c ? (c.params || "") : "";
  $("sqlConnVer").value = c ? (c.version_hint || "auto") : "auto";
  $("sqlConnEnabled").checked = c ? c.enabled !== false : true;
  const slow = (c && c.slow_sql) || {};
  const sched = slow.schedule || {};
  const slowEnabled = $("sqlConnSlowEnabled");
  // Default Slow SQL off for new/missing config; only auto-check when explicitly enabled.
  if (slowEnabled) slowEnabled.checked = !!(c && c.slow_sql && slow.enabled);
  const slowAlert = $("sqlConnSlowAlert");
  if (slowAlert) slowAlert.checked = c && c.slow_sql ? !slow.alert_disabled : false;
  const kind = $("sqlConnSlowKind"); if (kind) kind.value = sched.kind || "daily";
  const at = $("sqlConnSlowAt"); if (at) at.value = sched.at || "03:00";
  const iv = $("sqlConnSlowInterval"); if (iv) iv.value = sched.interval_min || 1440;
  const wd = $("sqlConnSlowWeekday"); if (wd) wd.value = sched.weekday != null ? sched.weekday : 1;
  const topn = $("sqlConnSlowTopN"); if (topn) topn.value = slow.top_n || 30;
  const minAvg = $("sqlConnSlowMinAvg"); if (minAvg) minAvg.value = slow.min_avg_latency_ms != null ? slow.min_avg_latency_ms : 100;
  const tr = $("sqlConnTestResult"); if (tr) { tr.textContent = ""; tr.className = "ai-test-result"; }
  $("sqlConnMask").classList.add("show");
}

function syncSQLConnDriverUI() {
  const drv = ($("sqlConnDriver") && $("sqlConnDriver").value) || "mysql";
  const isPG = drv === "postgres";
  const port = $("sqlConnPort");
  if (port && (!port.value || port.value === "3306" || port.value === "5432")) {
    port.value = isPG ? 5432 : 3306;
  }
  const verField = $("sqlConnVerField");
  if (verField) verField.style.display = isPG ? "none" : "";
  const params = $("sqlConnParams");
  if (params && !params.value) params.placeholder = isPG ? "sslmode=require&search_path=public" : "charset=utf8mb4";
  const tls = $("sqlConnTLS");
  if (tls && !tls.value) tls.placeholder = isPG ? "disable / prefer / require / verify-full" : "true / skip-verify / 空";
}

function collectSQLConn() {
  const kind = ($("sqlConnSlowKind") && $("sqlConnSlowKind").value) || "daily";
  const schedule = {
    enabled: !!($("sqlConnSlowEnabled") && $("sqlConnSlowEnabled").checked),
    kind,
    at: ($("sqlConnSlowAt") && $("sqlConnSlowAt").value.trim()) || "03:00",
    interval_min: parseInt(($("sqlConnSlowInterval") && $("sqlConnSlowInterval").value) || "1440", 10) || 1440,
    weekday: parseInt(($("sqlConnSlowWeekday") && $("sqlConnSlowWeekday").value) || "1", 10) || 0,
  };
  const driver = ($("sqlConnDriver") && $("sqlConnDriver").value) || "mysql";
  return {
    id: $("sqlConnId").value,
    name: $("sqlConnName").value.trim(),
    env: $("sqlConnEnv").value,
    driver,
    host: $("sqlConnHost").value.trim(),
    port: parseInt($("sqlConnPort").value, 10) || (driver === "postgres" ? 5432 : 3306),
    user: $("sqlConnUser").value.trim(),
    password: $("sqlConnPass").value,
    database: $("sqlConnDB").value.trim(),
    tls: $("sqlConnTLS").value.trim(),
    params: $("sqlConnParams").value.trim(),
    version_hint: $("sqlConnVer").value,
    enabled: $("sqlConnEnabled").checked,
    slow_sql: {
      enabled: !!($("sqlConnSlowEnabled") && $("sqlConnSlowEnabled").checked),
      alert_disabled: !($("sqlConnSlowAlert") && $("sqlConnSlowAlert").checked),
      schedule,
      top_n: parseInt(($("sqlConnSlowTopN") && $("sqlConnSlowTopN").value) || "30", 10) || 30,
      min_avg_latency_ms: parseFloat(($("sqlConnSlowMinAvg") && $("sqlConnSlowMinAvg").value) || "100") || 100,
    },
  };
}

async function saveSQLConn() {
  const c = collectSQLConn();
  if (!c.name || !c.host) { toast(sqlT("sql.name_host_required", "名称和主机必填"), "err"); return; }
  await withLoading("sqlConnSaveBtn", async () => {
    try {
      const editing = !!c.id;
      const url = editing ? `${API}/sql/connections/${encodeURIComponent(c.id)}` : `${API}/sql/connections`;
      const r = await fetch(url, { method: editing ? "PUT" : "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(c) });
      const j = await r.json().catch(() => ({}));
      if (r.ok) {
        toast(sqlT("ui.saved", "已保存"), "ok");
        $("sqlConnMask").classList.remove("show");
        await loadSQLConnections();
        renderSQLConnSelect();
        renderSQLConnList();
      } else toast(j.error || "保存失败", "err");
    } catch (e) { toast(String(e), "err"); }
  });
}

async function testSQLConnById(id) {
  toast(sqlT("sql.testing", "测试中…"), "ok");
  try {
    const r = await fetch(`${API}/sql/connections/${encodeURIComponent(id)}/test`, { method: "POST" });
    const j = await r.json().catch(() => ({}));
    if (j.ok) toast("✓ " + (j.version || "ok"), "ok");
    else toast("✗ " + (j.error || "失败"), "err");
  } catch (e) { toast("✗ " + e, "err"); }
}

async function deleteSQLConn(id) {
  if (!confirm(sqlT("sql.confirm_del", "确定删除该连接？"))) return;
  try {
    const r = await fetch(`${API}/sql/connections/${encodeURIComponent(id)}`, { method: "DELETE" });
    if (r.ok) {
      toast(sqlT("ui.deleted", "已删除"), "ok");
      await loadSQLConnections();
      renderSQLConnSelect();
      renderSQLConnList();
    } else toast("删除失败", "err");
  } catch (e) { toast(String(e), "err"); }
}

safeAddEventListener("sqlInnerTabs", "click", e => {
  const t = e.target.closest("[data-sql-tab]"); if (!t) return;
  showSQLTab(t.dataset.sqlTab);
});
safeAddEventListener("sqlAnalyzeBtn", "click", () => runSQLAnalyze());
safeAddEventListener("sqlRunBtn", "click", () => runSQLQuery());
safeAddEventListener("sqlCancelBtn", "click", () => cancelSQLQuery());
// 结果区的翻页 / 展开 / 复制 / 导出：结果是重画出来的，只能走事件委托。
safeAddEventListener("sqlResultPanel", "click", (e) => { onSQLResultAction(e); });
safeAddEventListener("sqlBeautifyBtn", "click", () => runSQLBeautify());
safeAddEventListener("sqlAuditBtn", "click", () => runSQLAudit());
safeAddEventListener("sqlOptimizeBtn", "click", () => runSQLOptimize());
safeAddEventListener("sqlExplainBtn", "click", () => runSQLExplain());
safeAddEventListener("sqlEditor", "keydown", e => {
  if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
    e.preventDefault();
    runSQLQuery();
  }
});
safeAddEventListener("sqlSubmitChangeBtn", "click", () => submitSQLChangeFromEditor());
safeAddEventListener("sqlChangeRefreshBtn", "click", async () => { await loadSQLChangeRequests(); renderSQLChangeList(); });
safeAddEventListener("sqlChangeList", "click", e => {
  const b = e.target.closest("[data-sqlchange]"); if (!b) return;
  actSQLChange(b.dataset.id, b.dataset.sqlchange);
});
safeAddEventListener("sqlAIBeautifyBtn", "click", () => openSQLAI("sql_beautify"));
safeAddEventListener("sqlAIAuditBtn", "click", () => openSQLAI("sql_audit"));
safeAddEventListener("sqlAIOptimizeBtn", "click", () => openSQLAI("sql_remediation"));
safeAddEventListener("addSQLConnBtn", "click", () => openSQLConnModal(null));
safeAddEventListener("sqlConnDriver", "change", syncSQLConnDriverUI);
safeAddEventListener("sqlConnSaveBtn", "click", saveSQLConn);
safeAddEventListener("sqlSlowRunBtn", "click", runSQLSlowCheck);
safeAddEventListener("sqlProcessRefreshBtn", "click", loadSQLProcessLocks);
safeAddEventListener("sqlSchemaHealthBtn", "click", loadSQLSchemaHealth);
safeAddEventListener("sqlSlowRefreshBtn", "click", loadSQLSlowLatest);
safeAddEventListener("sqlSlowSearch", "input", () => {
  SQL_SLOW_Q = (($("sqlSlowSearch") && $("sqlSlowSearch").value) || "");
  SQL_SLOW_PAGE = 1;
  refreshSQLSlowList();
});
safeAddEventListener("sqlSlowSort", "change", () => {
  SQL_SLOW_SORT = (($("sqlSlowSort") && $("sqlSlowSort").value) || "avg_desc");
  SQL_SLOW_PAGE = 1;
  refreshSQLSlowList();
});
safeAddEventListener("sqlSlowTypeFilter", "click", e => {
  const btn = e.target.closest("[data-slow-type]");
  if (!btn) return;
  SQL_SLOW_TYPE = btn.dataset.slowType || "";
  SQL_SLOW_PAGE = 1;
  refreshSQLSlowList();
});
safeAddEventListener("sqlSlowPanel", "click", e => {
  const b = e.target.closest("[data-pg]");
  if (!b || !b.dataset.pg) return;
  if (b.dataset.pg === "prev") SQL_SLOW_PAGE--;
  else if (b.dataset.pg === "next") SQL_SLOW_PAGE++;
  refreshSQLSlowList();
});
safeAddEventListener("sqlSlowPanel", "change", e => {
  if (e.target && e.target.dataset && e.target.dataset.pg === "size") {
    SQL_SLOW_SIZE = +e.target.value || 20;
    SQL_SLOW_PAGE = 1;
    refreshSQLSlowList();
  }
});
safeAddEventListener("sqlConnList", "click", e => {
  const b = e.target.closest("[data-sqlconn]"); if (!b) return;
  const id = b.dataset.id;
  if (b.dataset.sqlconn === "edit") {
    const c = SQL_CONNS.find(x => x.id === id);
    if (c) openSQLConnModal(c);
  } else if (b.dataset.sqlconn === "del") deleteSQLConn(id);
  else if (b.dataset.sqlconn === "test") testSQLConnById(id);
});

window._pageRenderers = window._pageRenderers || {};
window._pageRenderers["sql-toolkit"] = loadSQLToolkit;
