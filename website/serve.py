#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
AIOps 营销网站本地服务：静态资源 + SQLite 持久化 API。

用法:
  python website/serve.py [port]
  WEBSITE_ADMIN_PASSWORD=your-secret python website/serve.py 8090

数据文件默认: website/data/website.db
"""
from __future__ import annotations

import hashlib
import hmac
import json
import os
import re
import secrets
import sqlite3
import sys
import threading
import time
from datetime import datetime, timezone
from http import HTTPStatus
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Optional
from urllib.parse import parse_qs, urlparse

ROOT = Path(__file__).resolve().parent
DATA_DIR = ROOT / "data"
DB_PATH = Path(os.environ.get("WEBSITE_DB", str(DATA_DIR / "website.db")))
ADMIN_PASSWORD = os.environ.get("WEBSITE_ADMIN_PASSWORD", "aiops2026")
SESSION_TTL_SEC = int(os.environ.get("WEBSITE_ADMIN_TTL", "86400"))  # 24h
TOKEN_COOKIE = "aiops_admin_token"
MAX_BODY = 512 * 1024

EMAIL_RE = re.compile(r"^[^@\s]+@[^@\s]+\.[^@\s]+$")
PHONE_RE = re.compile(r"^\d{7,15}$")

_db_lock = threading.RLock()
_sessions: dict[str, float] = {}  # token -> expires_at
_sessions_lock = threading.Lock()


def utc_now_iso() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat()


def ensure_db() -> None:
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    with _db_lock:
        conn = sqlite3.connect(DB_PATH)
        try:
            conn.executescript(
                """
                PRAGMA journal_mode=WAL;
                PRAGMA synchronous=NORMAL;

                CREATE TABLE IF NOT EXISTS visits (
                  id INTEGER PRIMARY KEY AUTOINCREMENT,
                  page TEXT,
                  path TEXT,
                  ts TEXT NOT NULL,
                  enter_ts INTEGER,
                  exit_ts INTEGER,
                  duration INTEGER,
                  scroll INTEGER,
                  referrer TEXT,
                  session_id TEXT,
                  ip TEXT,
                  country TEXT,
                  region TEXT,
                  city TEXT,
                  org TEXT,
                  ua TEXT,
                  os TEXT,
                  os_version TEXT,
                  browser TEXT,
                  browser_version TEXT,
                  device_type TEXT,
                  screen TEXT,
                  viewport TEXT,
                  lang TEXT,
                  platform TEXT,
                  timezone TEXT,
                  utm_source TEXT,
                  utm_medium TEXT,
                  utm_campaign TEXT,
                  utm_term TEXT,
                  utm_content TEXT,
                  interactions_json TEXT,
                  raw_json TEXT,
                  created_at TEXT NOT NULL
                );
                CREATE INDEX IF NOT EXISTS idx_visits_ts ON visits(ts);
                CREATE INDEX IF NOT EXISTS idx_visits_session ON visits(session_id);
                CREATE INDEX IF NOT EXISTS idx_visits_ip ON visits(ip);
                CREATE INDEX IF NOT EXISTS idx_visits_page ON visits(page);

                CREATE TABLE IF NOT EXISTS contacts (
                  id INTEGER PRIMARY KEY AUTOINCREMENT,
                  name TEXT,
                  email TEXT NOT NULL,
                  phone TEXT,
                  message TEXT,
                  source TEXT,
                  ip TEXT,
                  ua TEXT,
                  created_at TEXT NOT NULL,
                  updated_at TEXT
                );
                CREATE INDEX IF NOT EXISTS idx_contacts_email ON contacts(email);
                CREATE INDEX IF NOT EXISTS idx_contacts_created ON contacts(created_at);

                CREATE TABLE IF NOT EXISTS subscribers (
                  id INTEGER PRIMARY KEY AUTOINCREMENT,
                  email TEXT NOT NULL UNIQUE,
                  phone TEXT,
                  source TEXT,
                  ip TEXT,
                  ua TEXT,
                  created_at TEXT NOT NULL,
                  updated_at TEXT
                );
                CREATE INDEX IF NOT EXISTS idx_subs_created ON subscribers(created_at);

                CREATE TABLE IF NOT EXISTS access_log (
                  id INTEGER PRIMARY KEY AUTOINCREMENT,
                  method TEXT,
                  path TEXT,
                  ip TEXT,
                  ua TEXT,
                  status INTEGER,
                  referer TEXT,
                  ts TEXT NOT NULL
                );
                CREATE INDEX IF NOT EXISTS idx_access_ts ON access_log(ts);

                CREATE TABLE IF NOT EXISTS admin_sessions (
                  token TEXT PRIMARY KEY,
                  created_at REAL NOT NULL,
                  expires_at REAL NOT NULL
                );
                """
            )
            conn.commit()
            # load durable sessions
            now = time.time()
            rows = conn.execute(
                "SELECT token, expires_at FROM admin_sessions WHERE expires_at > ?",
                (now,),
            ).fetchall()
            with _sessions_lock:
                for token, exp in rows:
                    _sessions[token] = float(exp)
            conn.execute("DELETE FROM admin_sessions WHERE expires_at <= ?", (now,))
            conn.commit()
        finally:
            conn.close()


def db() -> sqlite3.Connection:
    conn = sqlite3.connect(DB_PATH, timeout=30)
    conn.row_factory = sqlite3.Row
    return conn


def client_ip(handler: "WebsiteHandler") -> str:
    # Prefer reverse-proxy headers when present.
    xff = handler.headers.get("X-Forwarded-For") or handler.headers.get("X-Real-IP")
    if xff:
        return xff.split(",")[0].strip()
    return handler.client_address[0] if handler.client_address else ""


def read_json(handler: "WebsiteHandler") -> Any:
    length = int(handler.headers.get("Content-Length") or "0")
    if length <= 0:
        return {}
    if length > MAX_BODY:
        raise ValueError("body too large")
    raw = handler.rfile.read(length)
    if not raw:
        return {}
    return json.loads(raw.decode("utf-8"))


def json_response(handler: "WebsiteHandler", code: int, payload: Any) -> None:
    body = json.dumps(payload, ensure_ascii=False, default=str).encode("utf-8")
    handler.send_response(code)
    handler.send_header("Content-Type", "application/json; charset=utf-8")
    handler.send_header("Content-Length", str(len(body)))
    handler.send_header("Cache-Control", "no-store")
    handler.send_header("X-Content-Type-Options", "nosniff")
    handler.end_headers()
    handler.wfile.write(body)


def set_cookie(handler: "WebsiteHandler", name: str, value: str, max_age: int) -> None:
    handler.send_header(
        "Set-Cookie",
        f"{name}={value}; Path=/; HttpOnly; SameSite=Lax; Max-Age={max_age}",
    )


def clear_cookie(handler: "WebsiteHandler", name: str) -> None:
    handler.send_header(
        "Set-Cookie",
        f"{name}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0",
    )


def parse_cookies(handler: "WebsiteHandler") -> dict[str, str]:
    raw = handler.headers.get("Cookie") or ""
    out: dict[str, str] = {}
    for part in raw.split(";"):
        if "=" not in part:
            continue
        k, v = part.split("=", 1)
        out[k.strip()] = v.strip()
    return out


def issue_token() -> str:
    token = secrets.token_urlsafe(32)
    exp = time.time() + SESSION_TTL_SEC
    with _sessions_lock:
        _sessions[token] = exp
    with _db_lock:
        conn = db()
        try:
            conn.execute(
                "INSERT OR REPLACE INTO admin_sessions(token, created_at, expires_at) VALUES(?,?,?)",
                (token, time.time(), exp),
            )
            conn.commit()
        finally:
            conn.close()
    return token


def revoke_token(token: str) -> None:
    with _sessions_lock:
        _sessions.pop(token, None)
    with _db_lock:
        conn = db()
        try:
            conn.execute("DELETE FROM admin_sessions WHERE token=?", (token,))
            conn.commit()
        finally:
            conn.close()


def require_admin(handler: "WebsiteHandler") -> bool:
    cookies = parse_cookies(handler)
    token = cookies.get(TOKEN_COOKIE) or ""
    auth = handler.headers.get("Authorization") or ""
    if auth.lower().startswith("bearer "):
        token = auth[7:].strip()
    if not token:
        return False
    now = time.time()
    with _sessions_lock:
        exp = _sessions.get(token)
        if exp is None or exp < now:
            _sessions.pop(token, None)
            return False
    return True


def check_password(pwd: str) -> bool:
    a = hashlib.sha256(pwd.encode("utf-8")).digest()
    b = hashlib.sha256(ADMIN_PASSWORD.encode("utf-8")).digest()
    return hmac.compare_digest(a, b)


def row_to_visit(r: sqlite3.Row) -> dict[str, Any]:
    interactions = []
    try:
        interactions = json.loads(r["interactions_json"] or "[]")
    except Exception:
        interactions = []
    device = {
        "screen": r["screen"] or "",
        "viewport": r["viewport"] or "",
        "lang": r["lang"] or "",
        "platform": r["platform"] or "",
        "os": r["os"] or "",
        "osVersion": r["os_version"] or "",
        "browser": r["browser"] or "",
        "browserVersion": r["browser_version"] or "",
        "deviceType": r["device_type"] or "",
        "ua": r["ua"] or "",
        "tz": r["timezone"] or "",
    }
    geo = {
        "ip": r["ip"] or "",
        "country": r["country"] or "",
        "region": r["region"] or "",
        "city": r["city"] or "",
        "org": r["org"] or "",
    }
    utm = {
        "utm_source": r["utm_source"] or "",
        "utm_medium": r["utm_medium"] or "",
        "utm_campaign": r["utm_campaign"] or "",
        "utm_term": r["utm_term"] or "",
        "utm_content": r["utm_content"] or "",
    }
    # strip empty utm keys for admin UI compatibility
    utm = {k: v for k, v in utm.items() if v}
    return {
        "id": r["id"],
        "page": r["page"],
        "path": r["path"],
        "ts": r["ts"],
        "enterTs": r["enter_ts"],
        "exitTs": r["exit_ts"],
        "duration": r["duration"] or 0,
        "scroll": r["scroll"] or 0,
        "ref": r["referrer"] or "",
        "session": r["session_id"] or "",
        "interactions": interactions,
        "device": device,
        "geo": geo,
        "utm": utm,
        "ip": r["ip"] or "",
    }


def insert_visit(payload: dict[str, Any], ip: str, ua_hdr: str) -> int:
    device = payload.get("device") or {}
    geo = payload.get("geo") or {}
    utm = payload.get("utm") or {}
    interactions = payload.get("interactions") or []
    if not isinstance(interactions, list):
        interactions = []
    interactions = interactions[:80]
    ts = payload.get("ts") or utc_now_iso()
    # Prefer server-observed IP; keep client geo fields as enrichment.
    server_ip = ip or str(geo.get("ip") or "")
    with _db_lock:
        conn = db()
        try:
            cur = conn.execute(
                """
                INSERT INTO visits(
                  page, path, ts, enter_ts, exit_ts, duration, scroll, referrer, session_id,
                  ip, country, region, city, org, ua,
                  os, os_version, browser, browser_version, device_type,
                  screen, viewport, lang, platform, timezone,
                  utm_source, utm_medium, utm_campaign, utm_term, utm_content,
                  interactions_json, raw_json, created_at
                ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
                """,
                (
                    str(payload.get("page") or "")[:120],
                    str(payload.get("path") or "")[:500],
                    str(ts)[:64],
                    int(payload.get("enterTs") or 0) or None,
                    int(payload.get("exitTs") or 0) or None,
                    int(payload.get("duration") or 0),
                    int(payload.get("scroll") or 0),
                    str(payload.get("ref") or "")[:1000],
                    str(payload.get("session") or "")[:120],
                    server_ip[:80],
                    str(geo.get("country") or "")[:80],
                    str(geo.get("region") or "")[:80],
                    str(geo.get("city") or "")[:80],
                    str(geo.get("org") or "")[:160],
                    str(device.get("ua") or ua_hdr or "")[:300],
                    str(device.get("os") or "")[:40],
                    str(device.get("osVersion") or "")[:40],
                    str(device.get("browser") or "")[:40],
                    str(device.get("browserVersion") or "")[:40],
                    str(device.get("deviceType") or "")[:40],
                    str(device.get("screen") or "")[:40],
                    str(device.get("viewport") or "")[:40],
                    str(device.get("lang") or "")[:40],
                    str(device.get("platform") or "")[:80],
                    str(device.get("tz") or "")[:80],
                    str(utm.get("utm_source") or "")[:120],
                    str(utm.get("utm_medium") or "")[:120],
                    str(utm.get("utm_campaign") or "")[:120],
                    str(utm.get("utm_term") or "")[:120],
                    str(utm.get("utm_content") or "")[:120],
                    json.dumps(interactions, ensure_ascii=False),
                    json.dumps(payload, ensure_ascii=False, default=str)[:200000],
                    utc_now_iso(),
                ),
            )
            conn.commit()
            return int(cur.lastrowid)
        finally:
            conn.close()


def upsert_contact(payload: dict[str, Any], ip: str, ua: str) -> tuple[bool, str]:
    email = str(payload.get("email") or "").strip().lower()
    phone = re.sub(r"[\s\-\+]", "", str(payload.get("phone") or ""))
    name = str(payload.get("name") or "").strip()[:50]
    message = str(payload.get("message") or "").strip()[:500]
    source = str(payload.get("source") or "")[:500]
    if not EMAIL_RE.match(email):
        return False, "invalid_email"
    if not PHONE_RE.match(phone):
        return False, "invalid_phone"
    now = utc_now_iso()
    with _db_lock:
        conn = db()
        try:
            row = conn.execute(
                "SELECT id FROM contacts WHERE lower(email)=? ORDER BY id DESC LIMIT 1",
                (email,),
            ).fetchone()
            if row:
                conn.execute(
                    """
                    UPDATE contacts SET
                      name=CASE WHEN ?!='' THEN ? ELSE name END,
                      phone=CASE WHEN ?!='' THEN ? ELSE phone END,
                      message=CASE WHEN ?!='' THEN ? ELSE message END,
                      source=CASE WHEN ?!='' THEN ? ELSE source END,
                      ip=?, ua=?, updated_at=?
                    WHERE id=?
                    """,
                    (name, name, phone, phone, message, message, source, source, ip[:80], ua[:300], now, row["id"]),
                )
                conn.commit()
                return True, "updated"
            conn.execute(
                """
                INSERT INTO contacts(name,email,phone,message,source,ip,ua,created_at,updated_at)
                VALUES(?,?,?,?,?,?,?,?,?)
                """,
                (name, email, phone, message, source, ip[:80], ua[:300], now, now),
            )
            conn.commit()
            return True, "created"
        finally:
            conn.close()


def upsert_subscriber(payload: dict[str, Any], ip: str, ua: str) -> tuple[bool, str]:
    email = str(payload.get("email") or "").strip().lower()
    phone = re.sub(r"[\s\-\+]", "", str(payload.get("phone") or ""))
    source = str(payload.get("source") or "")[:500]
    if not EMAIL_RE.match(email):
        return False, "invalid_email"
    now = utc_now_iso()
    with _db_lock:
        conn = db()
        try:
            row = conn.execute("SELECT id FROM subscribers WHERE lower(email)=?", (email,)).fetchone()
            if row:
                conn.execute(
                    """
                    UPDATE subscribers SET
                      phone=CASE WHEN ?!='' THEN ? ELSE phone END,
                      source=CASE WHEN ?!='' THEN ? ELSE source END,
                      ip=?, ua=?, updated_at=?
                    WHERE id=?
                    """,
                    (phone, phone, source, source, ip[:80], ua[:300], now, row["id"]),
                )
                conn.commit()
                return True, "updated"
            conn.execute(
                """
                INSERT INTO subscribers(email,phone,source,ip,ua,created_at,updated_at)
                VALUES(?,?,?,?,?,?,?)
                """,
                (email, phone, source, ip[:80], ua[:300], now, now),
            )
            conn.commit()
            return True, "created"
        finally:
            conn.close()


def log_access(method: str, path: str, ip: str, ua: str, status: int, referer: str) -> None:
    # Skip noisy static assets
    if any(path.endswith(ext) for ext in (".css", ".js", ".png", ".svg", ".ico", ".woff2", ".map")):
        return
    with _db_lock:
        conn = db()
        try:
            conn.execute(
                "INSERT INTO access_log(method,path,ip,ua,status,referer,ts) VALUES(?,?,?,?,?,?,?)",
                (method[:16], path[:500], ip[:80], ua[:300], int(status), referer[:500], utc_now_iso()),
            )
            conn.commit()
        finally:
            conn.close()


def days_cutoff_iso(days: int) -> Optional[str]:
    if days <= 0:
        return None
    # compare ISO strings; use epoch ms filter in SQL via ts field ISO
    cutoff = time.time() - days * 86400
    return datetime.fromtimestamp(cutoff, timezone.utc).replace(microsecond=0).isoformat()


class WebsiteHandler(SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=str(ROOT), **kwargs)

    def end_headers(self):
        self.send_header("Cache-Control", "no-cache, no-store, must-revalidate")
        self.send_header("Pragma", "no-cache")
        self.send_header("Expires", "0")
        super().end_headers()

    def log_message(self, fmt: str, *args: Any) -> None:
        sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))

    def _finish_api(self, code: int, payload: Any, cookies: Optional[list[tuple[str, str, int]]] = None) -> None:
        body = json.dumps(payload, ensure_ascii=False, default=str).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        if cookies:
            for name, value, max_age in cookies:
                if max_age <= 0:
                    self.send_header(
                        "Set-Cookie",
                        f"{name}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0",
                    )
                else:
                    self.send_header(
                        "Set-Cookie",
                        f"{name}={value}; Path=/; HttpOnly; SameSite=Lax; Max-Age={max_age}",
                    )
        self.end_headers()
        self.wfile.write(body)
        try:
            log_access(
                self.command,
                urlparse(self.path).path,
                client_ip(self),
                self.headers.get("User-Agent") or "",
                code,
                self.headers.get("Referer") or "",
            )
        except Exception:
            pass

    def do_OPTIONS(self) -> None:
        self.send_response(204)
        self.send_header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type, Authorization")
        self.send_header("Access-Control-Allow-Credentials", "true")
        self.end_headers()

    def do_GET(self) -> None:
        parsed = urlparse(self.path)
        path = parsed.path
        if path.startswith("/api/"):
            self.handle_api_get(path, parse_qs(parsed.query))
            return
        # pretty routes
        if path in ("", "/"):
            self.path = "/index.html"
        elif path.endswith("/") is False and "." not in Path(path).name:
            candidate = ROOT / (path.lstrip("/") + ".html")
            if candidate.is_file():
                self.path = path + ".html"
        super().do_GET()
        try:
            log_access(
                "GET",
                path,
                client_ip(self),
                self.headers.get("User-Agent") or "",
                getattr(self, "_last_status", 200) or 200,
                self.headers.get("Referer") or "",
            )
        except Exception:
            pass

    def send_response(self, code, message=None):
        self._last_status = code
        super().send_response(code, message)

    def do_POST(self) -> None:
        parsed = urlparse(self.path)
        path = parsed.path
        if path.startswith("/api/"):
            self.handle_api_post(path)
            return
        self._finish_api(HTTPStatus.NOT_FOUND, {"error": "not_found"})

    def do_DELETE(self) -> None:
        parsed = urlparse(self.path)
        path = parsed.path
        if path.startswith("/api/"):
            self.handle_api_delete(path)
            return
        self._finish_api(HTTPStatus.NOT_FOUND, {"error": "not_found"})

    def handle_api_get(self, path: str, qs: dict[str, list[str]]) -> None:
        if path == "/api/health":
            self._finish_api(200, {"ok": True, "db": str(DB_PATH), "time": utc_now_iso()})
            return

        if not require_admin(self) and path.startswith("/api/admin/"):
            self._finish_api(HTTPStatus.UNAUTHORIZED, {"error": "unauthorized"})
            return

        days = 0
        try:
            days = int((qs.get("days") or ["0"])[0])
        except Exception:
            days = 0
        cutoff = days_cutoff_iso(days)

        if path == "/api/admin/visits":
            with _db_lock:
                conn = db()
                try:
                    if cutoff:
                        rows = conn.execute(
                            "SELECT * FROM visits WHERE ts >= ? ORDER BY id DESC LIMIT 5000",
                            (cutoff,),
                        ).fetchall()
                    else:
                        rows = conn.execute(
                            "SELECT * FROM visits ORDER BY id DESC LIMIT 5000"
                        ).fetchall()
                finally:
                    conn.close()
            self._finish_api(200, {"items": [row_to_visit(r) for r in rows]})
            return

        if path == "/api/admin/contacts":
            with _db_lock:
                conn = db()
                try:
                    rows = conn.execute(
                        "SELECT * FROM contacts ORDER BY id DESC LIMIT 5000"
                    ).fetchall()
                finally:
                    conn.close()
            items = [
                {
                    "id": r["id"],
                    "name": r["name"] or "",
                    "email": r["email"] or "",
                    "phone": r["phone"] or "",
                    "message": r["message"] or "",
                    "source": r["source"] or "",
                    "ip": r["ip"] or "",
                    "ua": r["ua"] or "",
                    "created_at": r["created_at"],
                    "updated_at": r["updated_at"],
                }
                for r in rows
            ]
            self._finish_api(200, {"items": items})
            return

        if path == "/api/admin/subscribers":
            with _db_lock:
                conn = db()
                try:
                    rows = conn.execute(
                        "SELECT * FROM subscribers ORDER BY id DESC LIMIT 5000"
                    ).fetchall()
                finally:
                    conn.close()
            items = [
                {
                    "id": r["id"],
                    "email": r["email"] or "",
                    "phone": r["phone"] or "",
                    "source": r["source"] or "",
                    "ip": r["ip"] or "",
                    "ua": r["ua"] or "",
                    "created_at": r["created_at"],
                    "updated_at": r["updated_at"],
                }
                for r in rows
            ]
            self._finish_api(200, {"items": items})
            return

        if path == "/api/admin/access-log":
            with _db_lock:
                conn = db()
                try:
                    if cutoff:
                        rows = conn.execute(
                            "SELECT * FROM access_log WHERE ts >= ? ORDER BY id DESC LIMIT 2000",
                            (cutoff,),
                        ).fetchall()
                    else:
                        rows = conn.execute(
                            "SELECT * FROM access_log ORDER BY id DESC LIMIT 2000"
                        ).fetchall()
                finally:
                    conn.close()
            items = [dict(r) for r in rows]
            self._finish_api(200, {"items": items})
            return

        if path == "/api/admin/export":
            with _db_lock:
                conn = db()
                try:
                    visits = [row_to_visit(r) for r in conn.execute("SELECT * FROM visits ORDER BY id").fetchall()]
                    contacts = [dict(r) for r in conn.execute("SELECT * FROM contacts ORDER BY id").fetchall()]
                    subs = [dict(r) for r in conn.execute("SELECT * FROM subscribers ORDER BY id").fetchall()]
                    access = [dict(r) for r in conn.execute("SELECT * FROM access_log ORDER BY id").fetchall()]
                finally:
                    conn.close()
            self._finish_api(
                200,
                {
                    "exportedAt": utc_now_iso(),
                    "visits": visits,
                    "contacts": contacts,
                    "subscribers": subs,
                    "access_log": access,
                },
            )
            return

        if path == "/api/admin/stats":
            with _db_lock:
                conn = db()
                try:
                    if cutoff:
                        vcount = conn.execute("SELECT COUNT(*) FROM visits WHERE ts>=?", (cutoff,)).fetchone()[0]
                        ccount = conn.execute("SELECT COUNT(*) FROM contacts WHERE created_at>=?", (cutoff,)).fetchone()[0]
                        scount = conn.execute("SELECT COUNT(*) FROM subscribers WHERE created_at>=?", (cutoff,)).fetchone()[0]
                    else:
                        vcount = conn.execute("SELECT COUNT(*) FROM visits").fetchone()[0]
                        ccount = conn.execute("SELECT COUNT(*) FROM contacts").fetchone()[0]
                        scount = conn.execute("SELECT COUNT(*) FROM subscribers").fetchone()[0]
                    db_size = DB_PATH.stat().st_size if DB_PATH.exists() else 0
                finally:
                    conn.close()
            self._finish_api(
                200,
                {
                    "visits": vcount,
                    "contacts": ccount,
                    "subscribers": scount,
                    "db_bytes": db_size,
                    "db_path": str(DB_PATH),
                },
            )
            return

        self._finish_api(HTTPStatus.NOT_FOUND, {"error": "not_found"})

    def handle_api_post(self, path: str) -> None:
        ip = client_ip(self)
        ua = self.headers.get("User-Agent") or ""
        try:
            payload = read_json(self)
        except Exception as e:
            self._finish_api(HTTPStatus.BAD_REQUEST, {"error": "invalid_json", "detail": str(e)})
            return
        if not isinstance(payload, dict):
            self._finish_api(HTTPStatus.BAD_REQUEST, {"error": "invalid_json"})
            return

        if path == "/api/visit":
            duration = int(payload.get("duration") or 0)
            if duration < 1:
                self._finish_api(200, {"ok": True, "skipped": True})
                return
            vid = insert_visit(payload, ip, ua)
            self._finish_api(200, {"ok": True, "id": vid})
            return

        if path == "/api/contact":
            ok, status = upsert_contact(payload, ip, ua)
            if not ok:
                self._finish_api(HTTPStatus.BAD_REQUEST, {"error": status})
                return
            self._finish_api(200, {"ok": True, "status": status})
            return

        if path == "/api/subscribe":
            ok, status = upsert_subscriber(payload, ip, ua)
            if not ok:
                self._finish_api(HTTPStatus.BAD_REQUEST, {"error": status})
                return
            self._finish_api(200, {"ok": True, "status": status})
            return

        if path == "/api/admin/login":
            pwd = str(payload.get("password") or "")
            if not check_password(pwd):
                time.sleep(0.4)
                self._finish_api(HTTPStatus.UNAUTHORIZED, {"error": "bad_password"})
                return
            token = issue_token()
            self._finish_api(
                200,
                {"ok": True, "token": token, "expires_in": SESSION_TTL_SEC},
                cookies=[(TOKEN_COOKIE, token, SESSION_TTL_SEC)],
            )
            return

        if path == "/api/admin/logout":
            cookies = parse_cookies(self)
            token = cookies.get(TOKEN_COOKIE) or str(payload.get("token") or "")
            if token:
                revoke_token(token)
            self._finish_api(200, {"ok": True}, cookies=[(TOKEN_COOKIE, "", 0)])
            return

        if path == "/api/admin/import-local":
            # One-shot migration from browser localStorage dump
            if not require_admin(self):
                self._finish_api(HTTPStatus.UNAUTHORIZED, {"error": "unauthorized"})
                return
            visits = payload.get("visits") or []
            contacts = payload.get("contacts") or []
            subscribers = payload.get("subscribers") or []
            n_v = n_c = n_s = 0
            if isinstance(visits, list):
                for v in visits[-5000:]:
                    if isinstance(v, dict):
                        insert_visit(v, str((v.get("geo") or {}).get("ip") or ip), ua)
                        n_v += 1
            if isinstance(contacts, list):
                for c in contacts:
                    if isinstance(c, dict):
                        upsert_contact(c, str(c.get("ip") or ip), ua)
                        n_c += 1
            if isinstance(subscribers, list):
                for s in subscribers:
                    if isinstance(s, dict):
                        upsert_subscriber(s, str(s.get("ip") or ip), ua)
                        n_s += 1
            self._finish_api(200, {"ok": True, "imported": {"visits": n_v, "contacts": n_c, "subscribers": n_s}})
            return

        self._finish_api(HTTPStatus.NOT_FOUND, {"error": "not_found"})

    def handle_api_delete(self, path: str) -> None:
        if not require_admin(self):
            self._finish_api(HTTPStatus.UNAUTHORIZED, {"error": "unauthorized"})
            return
        if path == "/api/admin/data":
            with _db_lock:
                conn = db()
                try:
                    conn.executescript(
                        """
                        DELETE FROM visits;
                        DELETE FROM contacts;
                        DELETE FROM subscribers;
                        DELETE FROM access_log;
                        """
                    )
                    conn.commit()
                finally:
                    conn.close()
            self._finish_api(200, {"ok": True})
            return
        self._finish_api(HTTPStatus.NOT_FOUND, {"error": "not_found"})


def main() -> None:
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8090
    ensure_db()
    os.chdir(ROOT)
    server = ThreadingHTTPServer(("0.0.0.0", port), WebsiteHandler)
    print(f"AIOps website + SQLite API")
    print(f"  URL : http://127.0.0.1:{port}/")
    print(f"  Admin: http://127.0.0.1:{port}/ethan.html")
    print(f"  DB  : {DB_PATH}")
    print(f"  Tip : set WEBSITE_ADMIN_PASSWORD to override default admin password")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nstopped")


if __name__ == "__main__":
    main()
