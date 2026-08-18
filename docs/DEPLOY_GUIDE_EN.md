# AIOps — Deployment Guide (Production)

> This document is for **Ops / SRE responsible for production deployment**. On top of the install steps in
> [INSTALL_EN.md](INSTALL_EN.md), it covers **production architecture, reverse proxy, gateway relay,
> cross-datacenter DR, security hardening, backup and scaling**. If you just want to get it running, read INSTALL_EN.md first.

---

## Table of Contents

- [1. Deployment topology](#1-deployment-topology)
- [2. Dual-storage deployment & production config](#2-dual-storage-deployment--production-config)
- [3. Reverse proxy (Nginx + HTTPS + terminal WebSocket)](#3-reverse-proxy-nginx--https--terminal-websocket)
- [4. Gateway relay (cross-subnet / behind-firewall management)](#4-gateway-relay-cross-subnet--behind-firewall-management)
- [5. Cross-datacenter DR (multi-server push)](#5-cross-datacenter-dr-multi-server-push)
- [6. Security hardening checklist](#6-security-hardening-checklist)
- [7. Config reference (server / agent)](#7-config-reference-server--agent)
- [8. Backup & restore](#8-backup--restore)
- [9. Scaling recommendations](#9-scaling-recommendations)
- [10. Deployment-level troubleshooting](#10-deployment-level-troubleshooting)

---

## 1. Deployment topology

```
                         ┌─────────────────────────────────────┐
   Browser / Android ───▶ │  Nginx (HTTPS termination + WS proxy) │
                         └───────────────┬─────────────────────┘
                                         │ :8529
                         ┌───────────────▼─────────────────────┐
                         │        aiops-server (Go binary)       │
                         │   Web UI / API / terminal / port-fwd  │
                         └───────┬───────────────────┬──────────┘
                                 │                   │
                  ┌──────────────▼─────┐    ┌─────────▼──────────┐
                  │  PostgreSQL (PG)   │    │  VictoriaMetrics    │
                  │ relational + audit │    │  time-series metrics│
                  │ + incidents/tickets│    │  (remote-write)     │
                  │ + pgvector         │    │                     │
                  └────────────────────┘    └─────────────────────┘

   Monitored-host Agent ──(X-Agent-Fingerprint)──▶ aiops-server
   Behind-firewall host ──▶ Relay gateway (X-Relay-Secret) ──▶ aiops-server
   Multi-DC Agent ──(servers[] broadcast + circuit breaker)──▶ DC-A / DC-B server
```

**Minimum production unit**: 1 `aiops-server` + 1 `PostgreSQL` + 1 `VictoriaMetrics`. All run single-instance;
horizontal scale-out is achieved via "multiple servers + Agent multi-server push".

---

## 2. Dual-storage deployment & production config

### 2.1 Dual-storage mandatory (neither can be omitted)

At boot the server validates these two env vars and calls `log.Fatal` if either is empty:

| Env Var | Purpose | Example |
|---|---|---|
| `AIOPS_POSTGRES_DSN` | PostgreSQL DSN, all relational data | `postgres://aiops:PWD@pg-host:5432/aiops?sslmode=disable` |
| `AIOPS_VM_URL` | VictoriaMetrics address, all time-series data | `http://vm-host:8428` |

**PostgreSQL minimum requirements**

- Enable the `pgvector` extension (RAG diagnostic vector search depends on it):
  ```sql
  CREATE EXTENSION IF NOT EXISTS vector;
  ```
- PostgreSQL 15+ recommended; image `pgvector/pgvector:pg18`.
- Production: `sslmode=require` (needs PG-side cert) and restrict source IPs.

**VictoriaMetrics minimum requirements**

- Any stable release; image `victoriametrics/victoria-metrics:latest`.
- Receives metrics via remote-write / Prometheus import. Retention adjustable (e.g. `-retentionPeriod=100y` ~ permanent, or `36` months).

> ⚠️ The built-in `aiops.db` is removed; there is no "works with only one store" fallback.

### 2.2 Server flags & optional env vars

| Flag / Env Var | Description |
|---|---|
| `-addr` (default `:8529`) | Listen address |
| `-config` (default `server_config.json`) | Server config file |
| `-dist` (default empty) | Agent binaries + `plugins.zip` dir for install commands |
| `-reset-admin-api=:PORT` | Local `127.0.0.1` admin password-reset API (emergency) |
| `AIOPS_SECRET_KEY` | At-rest master key, AES-256-GCM static encryption of sensitive secrets (strongly recommended) |
| `AIOPS_TLS_CERT` / `AIOPS_TLS_KEY` | Enable server-side HTTPS / TLS |
| `AIOPS_RELAY_SECRET` | Relay shared secret (maps to `relay_secret`) |
| `AIOPS_FORWARD_LISTEN` | Port-forward listen address (default `127.0.0.1`) |
| `AIOPS_FORWARD_PORT_RANGE` | Port-forward range (default `10100-10300`) |
| `AIOPS_FORWARD_DISABLED` | Disable port-forward (`true/false`) |

Key `server_config.json` fields: `install_token`, `require_token`, `mfa_required`, `relay_secret`,
`forward_listen`, `forward_port_range`, `account`, `checks`.

---

## 3. Reverse proxy (Nginx + HTTPS + terminal WebSocket)

In production, put `aiops-server` behind Nginx for HTTPS termination. **The remote terminal depends on WebSocket
upgrade + long connections; Nginx must forward the Upgrade header and disable buffering**, otherwise "metrics OK but terminal won't connect".

See `deploy/nginx-aiops.conf`:

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl;
    http2 on;
    server_name monitor.example.com;

    ssl_certificate     /etc/nginx/ssl/fullchain.pem;
    ssl_certificate_key /etc/nginx/ssl/privkey.pem;
    client_max_body_size 100m;   # match server maxBodyBytes (100MB), avoid 413 on large port-fwd files

    location / {
        proxy_pass http://127.0.0.1:8529;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Terminal WebSocket required
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_buffering         off;
        proxy_request_buffering off;
        proxy_read_timeout  86400s;
        proxy_send_timeout  86400s;
    }
}
```

> To let the server record real client IP for audit / login rate-limiting, set `"trust_proxy": true` in
> `server_config.json` (only behind a trusted proxy, to avoid spoofed IP headers).

---

## 4. Gateway relay (cross-subnet / behind-firewall management)

When monitored hosts are behind a firewall or across subnets and cannot reach the public server directly, deploy a
relay gateway on the **only internet-reachable machine**:

- The relay runs the Agent in Relay mode, listens on a local port, and reverse-proxies internal Agents' report / binary / terminal requests to the cloud server.
- The relay injects **`X-Relay-Secret`** when reporting to the server; the server verifies this secret to authenticate the relay source.
- Internal managed hosts just point `--server` at this relay — **no direct public-network access needed**.
- The gateway itself keeps collecting and reporting straight to the cloud while it relays, so it appears in the host list and can be upgraded / opened as a terminal like any other host. This needs a `token` in its `config.yaml`; the install script writes one. Gateways installed before this change: re-run the relay install command with `?token=` (config and /dl cache are kept).

Install (relay mode in "Install Agent", or similar):

```bash
curl -fsSL "https://cloud-server/install-relay.sh?token=TOKEN" | sudo sh
```

Agent-side config:

```yaml
relay: true
listen: ":8529"
relay_secret: "<must match AIOPS_RELAY_SECRET>"
```

> Relay and multi-server push are **mutually exclusive**: Relay is "one proxy to a single upstream"; multi-server is "one agent pushing to multiple upstreams". Don't enable both on the same Agent.

---

## 5. Cross-datacenter DR (multi-server push)

A single Agent can push to **multiple datacenter servers** at once: collected once, broadcast concurrently — this is **cross-datacenter disaster recovery**.

```yaml
# Agent config.yaml
servers:
  - server: "https://monitor-room-a:8529"
    token: "token-a"
  - server: "https://monitor-room-b:8529"
    token: "token-b"
```

- Each server has **independent auth / retry / connection pool**.
- The report channel has a built-in **circuit breaker + exponential backoff**: if one DC's server stays unreachable it is cut off and reconnected with backoff, without affecting the others.
- If any DC's server goes down, data still lands in the others and is backfilled after recovery. This is the core **cross-datacenter DR** mechanism (not an "active-active / multi-active" real-time sync semantic).

> In a multi-server setup, each server validates the Agent's `X-Agent-Fingerprint` independently.

---

## 6. Security hardening checklist

| Item | Recommendation |
|---|---|
| Transport encryption | Enable `AIOPS_TLS_CERT/KEY`, or place behind an Nginx HTTPS-terminating proxy; don't expose `8529` in plaintext |
| At-rest encryption | Set `AIOPS_SECRET_KEY` (long random, backed up); sensitive secrets AES-256-GCM at rest |
| Storage password | Strong PostgreSQL password + restricted source IPs; production `sslmode=require` |
| Session safety | Session tokens derived with PBKDF2-HMAC-SHA256 (600,000 iterations); login uses salted SHA-256 |
| Role permissions | Least-privilege admin / operator / viewer; port-forward needs operator+ role |
| MFA | Recommend global forced TOTP MFA; terminal secondary password protects sensitive ops |
| Install Token | Production `require_token=true`; rotate Tokens periodically (installed Agents unaffected) |
| Anti-clone fingerprint | Rely on `X-Agent-Fingerprint`; never copy `agent_state.json` across machines |
| Relay secret | Set `AIOPS_RELAY_SECRET` in relay scenarios and keep it safe |
| Port-forward | Listen `127.0.0.1` by default; set `0.0.0.0` only for Docker, with firewall-restricted range |
| Compliance | Audit / permissions / static encryption **align with MLPS audit requirements**; actual certification needs the full network + process assessment |

---

## 7. Config reference (server / agent)

### 7.1 Server `server_config.json` (excerpt)

```json
{
  "alerts_enabled": true,
  "install_token": "",
  "require_token": false,
  "mfa_required": false,
  "relay_secret": "",
  "forward_listen": "127.0.0.1",
  "forward_port_range": "10100-10300",
  "account": { "username": "admin", "display_name": "Administrator" }
}
```

### 7.2 Agent `config.yaml` (excerpt, covering common items)

```yaml
server: "http://localhost:8529"
token: ""
category: "Prod"
report_interval: 30
plugin_interval: 60
disk_path: "/"
plugins_dir: "plugins"
python: "python3"
state_file: "agent_state.json"

# Multi-server (overrides the single server + token above)
# servers:
#   - server: "https://monitor-a:8529"
#     token: "token-a"
#   - server: "https://monitor-b:8529"
#     token: "token-b"

log_encrypt: true
log_paths: []            # optional log-collection paths, glob supported

tls_skip_verify: false
ca_cert: ""              # self-signed CA PEM to verify server cert

# Gateway relay
relay: false
listen: ":8529"
relay_secret: ""
```

Optional collectors (commented = disabled, no resource use): Hyper-V, Redfish (BMC/iDRAC/iLO),
OceanStor (Huawei storage DeviceManager), NetFlow, nf_conntrack, SNMP (polling + Trap), SNI/DNS capture.
See `config.example.yaml` for the full list.

---

## 8. Backup & restore

- **PostgreSQL (core)**:
  ```bash
  docker exec aiops-pg pg_dump -U aiops aiops > pg-backup-$(date +%F).sql
  ```
  Restore: `docker exec -i aiops-pg psql -U aiops aiops < pg-backup.sql`.
  Contains all relational data, audit, incidents, tickets, users, secrets (secrets encrypted by `AIOPS_SECRET_KEY`; restore with the same master key).
- **VictoriaMetrics (time-series)**: back up the `./vm-data` directory, or use VM's official snapshot method.
- **Server config / terminal recordings**: the `./data` directory (contains `server_config.json`, `recordings/`, optional TLS certs).
- **`AIOPS_SECRET_KEY` must be backed up separately**: losing it means encrypted credentials in the DB cannot be decrypted.

---

## 9. Scaling recommendations

- A single instance, with gzip + pagination + multi-level downsampling + persistence, stably supports about **3000 monitored hosts**.
- Very large scale (10k+): externalize historical time-series to VictoriaMetrics with configurable retention; server-side `/hosts` pagination / incremental.
- Horizontal scale-out is not a "multi-replica shared storage" semantic; it distributes load across multiple independent servers via **Agent multi-server push (cross-datacenter DR)**.
- Agent base-metric collection is zero-dependency, so adding hosts needs no extra middleware.

---

## 10. Deployment-level troubleshooting

**Dual storage mandatory**
Server logs show `AIOPS_POSTGRES_DSN not set` or `AIOPS_VM_URL not set` → both must be set, otherwise `log.Fatal`.
Confirm both stores reachable and env vars injected (Docker: `environment:`; binary: `export`).

**Relay not working**
- `X-Relay-Secret` must match on both ends (Agent `relay_secret` ↔ server `AIOPS_RELAY_SECRET`).
- Relay listen address must be reachable by internal hosts; internal hosts' `--server` points at the relay, not the public server.

**One datacenter not writing**
- Check the Agent log circuit-breaker state; an unreachable DC triggers circuit break + backoff reconnect.
- Confirm that DC's server address, Token, and fingerprint admission policy are correct.

**Terminal won't connect (metrics fine)**
Almost always missing WebSocket config in the proxy: confirm Nginx forwards `Upgrade` / `Connection` headers, disables buffering, and lengthens timeouts (see Section 3).

**Port-forward no permission**
Creating / using port-forward needs an **operator-or-above** role; viewer is denied. Confirm the account role.

**Fingerprint conflict / duplicate hosts**
A cloned image copying `agent_state.json` triggers `host_id` regeneration; if still conflicting, delete the duplicate card and restart the Agent.

---

> For install steps, one-line Agent install, Android client, RBAC / MFA, see **[INSTALL_EN.md](INSTALL_EN.md)**.
