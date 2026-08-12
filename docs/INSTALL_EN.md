# AIOps — Installation Guide

> This document is for **Ops / SRE** readers who need to install and first-boot AIOps in a real environment.
> The server is a single Go binary (zero framework, zero CGO) with an embedded Web UI and API and no runtime
> external dependencies (other than storage). The collection Agent is a multi-platform native collector
> (Linux / Windows / macOS; other platforms go through plugins).

---

## 0. Architecture & Storage Notes (read first)

AIOps uses a **mandatory dual-storage architecture** — **neither can be omitted**:

| Storage | Purpose | Env Var | Minimum Requirement |
|---|---|---|---|
| **PostgreSQL** | Relational data: config / users / audit / incidents / tickets / sessions / secrets, plus RAG vectors (`pgvector`) | `AIOPS_POSTGRES_DSN` | `pgvector` extension enabled (PostgreSQL 15+ recommended, image `pgvector/pgvector:pg18`) |
| **VictoriaMetrics** | Time-series data: metrics / trends / SLO / probe results | `AIOPS_VM_URL` | Any stable release (image `victoriametrics/victoria-metrics`) |

> ⚠️ **Startup hard constraint**: at boot, if either `AIOPS_POSTGRES_DSN` or `AIOPS_VM_URL` is empty,
> the server calls `log.Fatal` and exits. There is **no fallback** to any local single-file database
> (the built-in `aiops.db` has been removed). Provision both stores before deploying.
>
> ⚠️ **Secret safety (strongly recommended)**: `AIOPS_SECRET_KEY` is used to encrypt MFA / SMTP / AI / webhook /
> relay secrets at rest with **AES-256-GCM** before they are written to the database. If unset, the server only
> warns and stores secrets in plaintext. Use a **long random string** and back it up — losing this key means
> encrypted credentials in the DB can no longer be decrypted.

### Default credentials & secure initialization

- Default admin: `admin / admin`.
- **First login forces a "security initialization" dialog** — you must change the username + password before entering. Cannot be skipped.
- It is recommended to enable **MFA (TOTP)** in user settings afterward.

### Ports & addresses

| Port | Purpose |
|---|---|
| `8529` | Web UI / API (server listen; adjustable via `-addr`) |
| `8428` | VictoriaMetrics UI / PromQL (expose only on internal networks) |
| `10100-10300` | TCP port-forward listen range (controlled by `AIOPS_FORWARD_LISTEN` / `forward_listen`) |

---

## 1. Prerequisites

- **Build / run server**: Go 1.22+ (`CGO_ENABLED=0` produces a fully static binary); at runtime only the two stores must be reachable.
- **PostgreSQL**: with the `pgvector` extension. Use image `pgvector/pgvector:pg18`, or on a self-managed instance run `CREATE EXTENSION IF NOT EXISTS vector;`.
- **VictoriaMetrics**: image `victoriametrics/victoria-metrics:latest`.
- **Collection Agent**:
  - Base metrics (CPU / memory / SWAP / multiple disks / network / load / process count / TCP connections) are collected **natively by the Go core with zero dependencies** — no Python required on any platform.
  - Only the **plugin layer** (service probing, anomaly detection, process monitoring, etc.) needs **Python 3 + psutil**, optional.
- **Android client**: Android Studio + Android SDK 34 + JDK 17 (see Section 5).

---

## 2. Option A: Docker Compose one-shot server (recommended)

The repo ships `docker-compose.yml` with a 3-container stack: `aiops-server` + `postgres` + `victoriametrics`.

```bash
# 1) Prepare env (change passwords/keys for production)
cp .env.example .env

# 2) Production: pull prebuilt images
docker compose up -d
# Optional local agent: docker compose --profile agent up -d

# Development (local Dockerfile.dev build + Docker Hub PG/VM):
# docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build

# 3) Logs & status
docker compose logs -f
docker compose ps
```

`docker-compose.yml` reads secrets from `.env` (`POSTGRES_PASSWORD`, `AIOPS_SECRET_KEY`, …) and wires
`AIOPS_VM_URL` / `AIOPS_POSTGRES_DSN` / `AIOPS_FORWARD_LISTEN`. **In production, be sure to**:

- Change `POSTGRES_PASSWORD` (compose builds the DSN from it);
- Replace `AIOPS_SECRET_KEY` with your own long random string and back it up;
- Or run `bash scripts/secure-compose.sh` (with `SKIP_DOWNLOAD=1` in a checkout) to generate them;
- To enable HTTPS, mount certs into `./data/tls` and uncomment `AIOPS_TLS_CERT` / `AIOPS_TLS_KEY`.

Stop (data retained; add `-v` to also remove volumes):

```bash
docker compose down
```

> Persistence: PostgreSQL uses a Docker named volume (more reliable than bind-mount on Windows/macOS, avoids WAL corruption);
> VM uses `./vm-data`; server config and terminal recordings use `./data`. Backup PG: `docker exec aiops-pg pg_dump ...`.

---

## 3. Option B: Single-binary server

The server is a single Go binary with no external runtime dependencies.

### 3.1 Build (Go, zero CGO)

```bash
# Server
CGO_ENABLED=0 go build -ldflags="-s -w" -o aiops-server ./cmd/server

# Agent (this platform)
CGO_ENABLED=0 go build -ldflags="-s -w" -o aiops-agent  ./cmd/agent

# Cross-compile Agents for other platforms
GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -o aiops-agent-linux-amd64   ./cmd/agent
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o aiops-agent.exe            ./cmd/agent
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -o aiops-agent-mac           ./cmd/agent
```

### 3.2 Run (env-var style)

```bash
export AIOPS_POSTGRES_DSN="postgres://aiops:PASSWORD@127.0.0.1:5432/aiops?sslmode=disable"
export AIOPS_VM_URL="http://127.0.0.1:8428"
export AIOPS_SECRET_KEY="aiops-$(head -c 44 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')"
# optional: enable TLS
# export AIOPS_TLS_CERT=/path/to/server.crt
# export AIOPS_TLS_KEY=/path/to/server.key

./aiops-server -addr 0.0.0.0:8529 \
  -config ./server_config.json \
  -dist ./dist
```

- `-addr`: listen address (default `:8529`).
- `-config`: server config path (default `server_config.json`, auto-generated on first boot).
- `-dist`: directory holding per-platform Agent binaries and `plugins.zip`, used by the one-line "Install Agent" command (`install.sh` / `install.ps1`). The repo `dist/` is prebuilt.
- `-reset-admin-api`: start a local admin password-reset API on `127.0.0.1:PORT` (emergency use).

### 3.3 Autostart (Linux systemd)

Use the `deploy/aiops-server.service` sample, adjust paths, then:

```bash
cp deploy/aiops-server.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now aiops-server
systemctl status aiops-server
journalctl -u aiops-server -f
```

### 3.4 Firewall

```bash
# firewalld
firewall-cmd --add-port=8529/tcp --permanent && firewall-cmd --reload
# ufw
ufw allow 8529/tcp
```

---

## 4. Collection Agent deployment

### 4.1 Recommended: one-line install (Token mode)

In the Web UI, top-right **"Install Agent"** → pick the target OS → copy one of the commands and run it on the monitored host.
The command already embeds the **server address** and **Token**, auto-downloads Agent + plugins, writes config, registers autostart, and comes online.

```bash
# Linux (root / sudo)
curl -fsSL "http://<SERVER_IP>:8529/install.sh?token=<TOKEN>" | sudo sh

# macOS
curl -fsSL "http://<SERVER_IP>:8529/install.sh?token=<TOKEN>" | sh

# Windows (admin PowerShell)
irm "http://<SERVER_IP>:8529/install.ps1?token=<TOKEN>" | iex
```

- Fill in a "host category" at install time to auto-group the new host.
- Click "Reset" in the UI to **rotate the Token** (old install commands become invalid), but **installed Agents are unaffected** (see 4.4 fingerprint auth).

### 4.2 Manual install (isolated networks / custom paths)

Each monitored host needs two things in the same directory:

```
aiops-agent(.exe / -mac)     # Agent binary for the platform
plugins/                     # plugin directory (copy whole; optional)
```

Run Agent from the directory that contains `plugins/`, or use `--plugins-dir` to point at an absolute plugin path (recommended when running as a service).

**Common flags**

| Flag | Description | Default |
|---|---|---|
| `--server` | Server address, e.g. `http://10.0.0.5:8529` | `http://localhost:8529` |
| `--token` | Install Token (injected by "Install Agent"; can be empty) | empty |
| `--category` | Host category, e.g. `Prod` / `DB` / `Office` (grouping & alert routing) | empty |
| `--interval` | Base metric report interval (seconds) | `30` |
| `--plugin-interval` | Plugin execution period (seconds) | `60` |
| `--plugins-dir` | Plugin directory (absolute path allowed) | `plugins` |
| `--python` | Interpreter for `.py` plugins | `python3` (Win: `python`) |
| `--disk-path` | Primary disk path (Windows: `C:\`) | `/` |
| `--config` | Config file path (see `config.example.yaml`) | `config.json` |
| `--ca-cert` | Self-signed CA PEM path to verify server cert | empty |
| `--tls-skip-verify` | Skip server cert verification (self-signed / internal only, unsafe) | `false` |

Or use a config file instead of flags: `cp config.example.yaml config.yaml`, edit, then run `aiops-agent`
(precedence: `config.yaml` → `config.yml` → `config.json`, or explicit `--config`).

**Python plugin dependency (optional)**: `pip install -r plugins/requirements.txt` (i.e. `psutil`).
Without it, base metrics still collect natively; only plugins are silently skipped.

### 4.3 Multi-server concurrent broadcast (cross-datacenter DR)

In `config.yaml`, use a `servers` array (when non-empty it **overrides** the single `server` + `token`):

```yaml
servers:
  - server: "https://monitor-a:8529"
    token: "token-a"
  - server: "https://monitor-b:8529"
    token: "token-b"
```

- Collected **once**, broadcast concurrently to all servers; each server has **independent auth / retry / connection pool**.
- The report channel has a built-in **circuit breaker + backoff**: if one server stays unreachable it is cut off and reconnected with exponential backoff, without affecting the others.
- This is the core of **cross-datacenter disaster recovery**: if one datacenter's server goes down, data still lands in the others.

### 4.4 Machine-fingerprint auth (anti-clone)

- At registration the Agent sends its machine fingerprint (`machine-id` + primary MAC, SHA-256, first 12 chars) to the server, bound via the **`X-Agent-Fingerprint`** header on every report/terminal request.
- All subsequent reports and terminal requests carry this fingerprint and **no longer depend on the install Token** — so **Token rotation does not affect installed Agents**.
- Each server validates the fingerprint **independently** in a multi-server setup.
- **Clone protection**: if a cloned image copies `agent_state.json`, the Agent auto-regenerates `host_id` to avoid identity conflicts.

**Token vs fingerprint admission policy:**

| Scenario | Behavior |
|---|---|
| New host, first registration | **Must** present a valid install Token (enforced when server `require_token=true`) |
| Known host, fingerprint matches | Token-free re-registration allowed (useful after server DB restore) |
| Known fingerprint within 7 days | Grace period allows token-free re-registration |
| Token rotated / expired | Only affects **new** install commands; installed Agents keep reporting by fingerprint, no config change needed |

### 4.5 Gateway relay mode

When only one machine in an internal network can reach the internet (behind a firewall / across subnets), install the Agent on that machine in **Relay mode**:
the relay listens on a local port and reverse-proxies internal Agents' requests to the cloud server, enabling management of hosts across subnets.

```bash
# Relay install command (obtained from "Install Agent" relay mode, or similar)
curl -fsSL "https://cloud-server/install-relay.sh?token=TOKEN" | sudo sh
```

- The relay injects **`X-Relay-Secret`** when reporting to the server; the server verifies this secret to authenticate the relay source.
- Config: Agent side `--relay`, `--relay-listen`, `-relay-secret` (or server `relay_secret` / `AIOPS_RELAY_SECRET`).
- Internal managed hosts just point `--server` at this relay; no direct public-network access needed.
- **Relay and multi-server push are mutually exclusive**: Relay is "one proxy to a single upstream"; multi-server is "one agent pushing to multiple upstreams".

### 4.6 Per-platform autostart

- **Windows**: NSSM (`nssm install AIOps-Agent ...`), or the repo `deploy/start-agent.bat` with Task Scheduler (working directory must contain `plugins/`).
- **Linux**: repo `deploy/aiops-agent.service` (systemd, `Restart=always`).
- **macOS**: repo `deploy/com.aiops.agent.plist` (launchd).

---

## 5. Android client (private self-hosted distribution)

> The Android app is **not published on any app store**; it uses **private self-hosted distribution** — you distribute the APK
> internally / on your enterprise channel. Source is in the repo `android/`; build it yourself with Android Studio.

**Build & distribute**

1. Android Studio → `File → Open` → select the repo `android/` directory.
2. Wait for Gradle Sync (depends on Compose BOM / Retrofit / OkHttp / DataStore).
3. `Build → Build Bundle(s) / APK(s) → Build APK`; distribute the resulting APK internally.

> ⚠️ **Not compiled/verified in sandbox**: this environment (CI / sandbox) has **not** compiled or run the Android project.
> Open `android/` in your local Android Studio and run `./gradlew assembleDebug` to verify before distributing, to avoid build-environment drift.

**Connect & use**

- In-app "Settings / Server" page: fill in the backend address (recommended `https://...`; internal HTTP: `http://192.168.1.10:8529`).
- Address and username are persisted via **DataStore** (`baseUrl` / `username`); not exported by system backup before app reinstall.
- For **http cleartext** backends, the app enables `usesCleartextTraffic`, so Android 9+ will not block it.
- Login uses `POST /api/v1/login`, returning the `aiops_session` Cookie; the app uses **dual-track Cookie persistence** (memory + DataStore), auto-cleared and re-login on server switch or session expiry.
- If the account has MFA enabled, login shows an **MFA OTP** dialog; entering the **terminal** requires re-verifying password or MFA OTP (terminal secondary password).
- Notifications use a **self-built `/ws/push`** long-connection push (no FCM/system push); foreground pages auto-refresh.

**Backend API contract (app-aligned, public endpoints)**

| Endpoint | Purpose |
|---|---|
| `POST /api/v1/login` | Login, returns `aiops_session` Cookie |
| `GET /api/v1/me` | Current session (401 if not logged in) |
| `GET /api/v1/hosts` | Host list (with `latest` metric snapshot, `online` state) |
| `GET /api/v1/hosts/{id}/metrics` | Host metric time series |
| `GET /api/v1/summary` | Overview stats |
| `GET /api/v1/alerts` | Current alerts |
| `POST /api/v1/alerts/ack` · `/silence` | Acknowledge / silence |
| `GET /api/v1/hosts/{id}/terminal` | Terminal WebSocket |
| `POST /api/user/terminal-password/verify` | Terminal secondary-password verify |
| `GET /api/v1/checks` · `POST /checks/{id}/run` | Probe list & run now |
| `GET/POST /api/v1/incidents` · `POST /incidents/{id}/ack\|resolve\|ticket` | Full incident lifecycle |
| `GET/POST /api/v1/tickets/{id}` | Ticket query & status update |
| `POST /api/v1/chat` | AI assistant SSE streaming chat (compatible with old `/ai/chat`) |

---

## 6. Security configuration

### 6.1 RBAC roles

- **admin**: all permissions, including user management (create / edit / delete / reset password / unbind MFA).
- **operator**: everything except user management (terminal / playbooks / config / host deletion, etc.).
- **viewer**: view only; can manage own profile / password / MFA.
- Route-level permission interception; user management UI is in the Web "User Management" (admin only).

> **Port-forward permission**: creating / using TCP port-forward rules requires an **operator-or-above (operator+)** role; viewer is denied.

### 6.2 Session & secret safety

- Login credentials use **salted SHA-256**; session tokens are derived with **PBKDF2-HMAC-SHA256 (600,000 iterations)** against offline brute-force.
- MFA / SMTP / AI / webhook / relay secrets are statically encrypted at rest with AES-256-GCM derived from `AIOPS_SECRET_KEY`.
- Optional **HTTPS / TLS**: set `AIOPS_TLS_CERT` / `AIOPS_TLS_KEY`; if not enabled, place behind an HTTPS-terminating proxy (e.g. Nginx) to avoid plaintext credentials / sessions / terminal data.

### 6.3 Optional MFA & terminal secondary password

- **MFA (TOTP)**: RFC 6238, Google Authenticator compatible; global forced MFA can be enabled.
- **Terminal secondary password**: before entering the remote terminal, re-verify password or MFA OTP — raises the bar on sensitive operations.
- **Account recovery**: unauthenticated two-factor recovery (email code + optional MFA) for username / password reset.

### 6.4 Compliance wording

The system's audit logs, operation traceability, role permissions, and static encryption **align with the audit requirements of China's MLPS (等保)**.
Actual MLPS certification must be assessed together with your network topology, physical security, and management processes — it cannot be "passed" by this software alone.

---

## 7. Verification

1. Open `http://<SERVER_IP>:8529`; first login goes through secure initialization (change username/password).
2. Within seconds the Agent host card appears under the correct **category group**, with CPU / memory / SWAP / per-disk / load / network / TCP connections / process count.
3. CLI self-check (on the server host):
   ```bash
   curl http://localhost:8529/api/v1/hosts | python3 -m json.tool
   curl http://localhost:8529/api/v1/summary
   ```
4. If VictoriaMetrics is enabled, visit `http://<SERVER_IP>:8428` and query metrics with PromQL.

---

## 8. FAQ

**① Server won't start: "AIOPS_POSTGRES_DSN not set / AIOPS_VM_URL not set"?**
Both stores are mandatory. PostgreSQL (`AIOPS_POSTGRES_DSN`) and VictoriaMetrics (`AIOPS_VM_URL`) must both be configured, otherwise `log.Fatal` exits. Confirm both stores are reachable and the env vars are injected.

**② Host not visible in the UI?**
- Check Agent logs for "report success"; if absent, it likely can't reach the server.
- Is server port `8529` open? Is `--server` (IP / port / http prefix) correct?
- With `require_token=true`, new hosts need a valid Token; known-fingerprint hosts are token-free (see 4.4).

**③ Agent fingerprint conflict / clone causing duplicate hosts?**
A cloned image copies `agent_state.json`; the Agent detects the conflict and **auto-regenerates `host_id`**, usually no duplicates. If still conflicting: delete the duplicate card and restart the Agent to re-register; avoid copying the same `agent_state.json` across machines.

**④ Token expired / rotated — will installed Agents drop?**
No. Installed Agents authenticate by **`X-Agent-Fingerprint`**, not by Token; Token only constrains **new** install commands. Rotating the Token only invalidates old install commands; online hosts need no change.

**⑤ Port-forward unreachable / no permission?**
- Forward port listens on `127.0.0.1` by default (local only). Docker deploys need `AIOPS_FORWARD_LISTEN=0.0.0.0` for host access.
- Creating / using port-forward needs an **operator-or-above** role; viewer is denied.
- Open firewall range `10100-10300`.

**⑥ Host appears but base metrics are 0?**
All three platforms collect natively; this should not happen normally. On minimal systems missing `/proc` or with restricted permissions, install `psutil` as a fallback.

**⑦ No custom metrics / plugin events?**
`plugins/` isn't next to the Agent, or `--plugins-dir` points wrong; or `psutil` isn't installed.

**⑧ How to do cross-datacenter DR?**
Use the `servers` array in Agent `config.yaml` pointing at multiple datacenter servers (see 4.3); the report channel has circuit breaker + backoff, so one datacenter going down doesn't stop writes to the others.

---

> For deployment architecture, production hardening, Nginx reverse proxy, backup and scaling, see **[DEPLOY_GUIDE_EN.md](DEPLOY_GUIDE_EN.md)**.
