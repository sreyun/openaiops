<div align="center">

# AIOps

**Self-hosted ops console without inbound ports: see hosts · open terminal/desktop · tame alerts**

[![Version](https://img.shields.io/badge/Version-v1.0.6-blue)](https://github.com/sreyun/aiops/releases/tag/v1.0.6)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS-lightgrey)]()

**[简体中文](README.md) · [繁體中文](README.zh-TW.md) · [English](README_EN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt-BR.md) · [Русский](README.ru.md)**

</div>

> **Language policy:** Landing READMEs cover the languages above. Deep install/deploy guides stay authoritative in [English](INSTALL_EN.md) and [中文](INSTALL.md); other-language READMEs focus on the 3-minute tip-of-the-spear path.

> Many machines sit behind NAT or strict firewalls — you can install an agent, but you cannot open inbound ports.  
> AIOps uses a **reverse-connecting agent** to put monitoring, web terminal/desktop, and alerting into one self-hosted control plane: `docker compose` for the server, one install command on each host.

**Current release [v1.0.6](https://github.com/sreyun/aiops/releases/tag/v1.0.6)** · Mirrors: [GitHub](https://github.com/sreyun/aiops) / [Gitee](https://gitee.com/bigdatasafe/aiops) · [CHANGELOG](CHANGELOG.md)

---

## Start here (about 3 minutes)

```bash
# 1) Bring up server + PostgreSQL (pgvector) + VictoriaMetrics
docker compose up -d

# 2) Open the UI and finish first-login security init
open http://localhost:8529

# 3) Copy the install command from the UI and run it on the target host
# (agent connects outbound — no inbound port on the host)
# curl -fsSL "http://<server>:8529/install.sh?token=<TOKEN>" | sudo sh
```

Then verify these three steps:

1. **Host appears online** with CPU / memory / disk metrics  
2. **Open Web Terminal** from the host card  
3. **Create one threshold alert** and confirm Feishu / DingTalk / email delivery  

That is the tip-of-the-spear path: a **reverse-connect ops console**. Everything else builds on it.

---

## Why AIOps

| | |
|---|---|
| **Reverse connect, less network change** | Agent dials out; terminal, desktop, and forwards share the same tunnel |
| **Single-binary server + zero-dep agent** | One Go server; stdlib agent on Linux / Windows / macOS / Kylin |
| **Your data stays yours** | PostgreSQL + VictoriaMetrics, AGPL-3.0 licensed, no feature gating |

> This is not “another uptime probe” or “another dashboard”. It collapses the stack small teams usually glue together — monitoring + alerting + remote troubleshooting — into one self-hosted platform.  
> The product can be broad; **the front door should stay narrow.**

---

## Contents

- [Start here](#start-here-about-3-minutes)
- [Why AIOps](#why-aiops)
- [Capability map](#capability-map)
- [Recent highlights](#recent-highlights)
- [Install](#install)
  - [Reverse proxy (Nginx) — read this before going public](#reverse-proxy-nginx--read-this-before-going-public)
- [Recommended path](#recommended-path)
- [Configuration](#configuration)
- [Architecture](#architecture)
- [Docs & boundaries](#docs--boundaries)
- [Contributing](#contributing)
- [License](#license)

---

## Capability map

Master the primary path first; expand only when you need to.

```
Primary path (start here)
  Host metrics → alert governance → web terminal / remote desktop → port forward

Platform extensions (same control plane)
  Probes / API monitors · logs · playbooks · SRE (incidents / SLO / tickets)
  AI inspect / Hermes / MCP · security center · Hyper-V / containers / K8s
  SNMP / NetFlow / Redfish · SQL toolkit · Android / HarmonyOS consoles*
```

\* Mobile clients are distributed separately; source is not in this repo.

<details>
<summary><b>Hosts & resources</b></summary>

- Native metrics: CPU / mem / disk / process / ports / net / DiskIO / IOPS / GPU / load  
- Out-of-band (no agent on target): Redfish, NetFlow, Huawei OceanStor, SNMP, container / Hyper-V / K8s inventory  
- Global resource search and topology helpers  

</details>

<details>
<summary><b>Alerts, probes & observability</b></summary>

- Thresholds + silence / inhibit / route; Feishu, DingTalk, email, SMS, voice  
- Probes: Ping / TCP / HTTP / process; API availability / P95 / throughput  
- Log tail (optional encrypted uplink) + search; time series on VictoriaMetrics  

</details>

<details>
<summary><b>Terminal, desktop & forward</b></summary>

- Web terminal: tabs, session replay, read-only watch, command audit, second password  
- Web remote desktop: JPEG / H.264; Windows lock screen needs **service install** (CAD / unlock)  
- Port forward / HTTP reverse proxy (incl. WebSocket) with SSRF egress guards  

</details>

<details>
<summary><b>Automation, SRE & AI</b></summary>

- Playbooks, gated auto-remediation, incidents / SLO / tickets, unified inbox  
- AI inspection & RCA (OpenAI-compatible); pgvector RAG; Hermes chat  
- MCP: expose read-only tools to Cursor / Claude; optional external MCP clients  

</details>

<details>
<summary><b>Security & mobile</b></summary>

- RBAC, optional MFA, audit, machine fingerprint, at-rest encryption, optional TLS, security center  
- Android / HarmonyOS consoles externally distributed; push via self-hosted long-lived connection  

</details>

---

## Recent highlights

| Area | Note |
|---|---|
| **Dual UI** | Classic dashboard + Vue console (`/v2/`); switch from the UI |
| **MCP** | Streamable HTTP duty / diagnosis tools; external MCP clients supported |
| **Agent remote update** | Batch rollout; Windows stays pending until version ACK |
| **Desktop hardening** | Windows lock / CAD / unlock path continuously improved (service install) |

Full notes: [CHANGELOG.md](CHANGELOG.md) · [Releases](https://github.com/sreyun/aiops/releases)

---

## Install

> The server **requires** both PostgreSQL and VictoriaMetrics.

### Docker Compose (recommended)

```bash
docker compose up -d

# Dev overlay (local build):
# docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build
```

Open `http://localhost:8529` and finish security initialization (forced password change). Enable MFA afterward.

Optional hardened bootstrap: [`scripts/secure-compose.sh`](scripts/secure-compose.sh)

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/sreyun/aiops/master/scripts/secure-compose.sh)
docker compose up -d
```

### Binary / from source

```bash
export AIOPS_POSTGRES_DSN="postgres://aiops:secret@localhost:5432/aiops?sslmode=disable"
export AIOPS_VM_URL="http://localhost:8428"
./aiops-server

cp config.example.yaml config.yaml
./aiops-agent --config config.yaml

# Go 1.26+
go build ./cmd/server ./cmd/agent
```

### Reverse proxy (Nginx) — read this before going public

The dashboard is ordinary HTTP, but **remote terminal, remote desktop, port forwarding and agent
auto-update** ride on long-lived streaming channels the agent dials out (agents have no inbound
port). Several nginx **defaults** break exactly those channels, and the symptom is misleading:
**hosts online, metrics fine, settings save fine — yet the terminal never connects and agent
auto-update always fails.**

Use the full example in the repo: [`deploy/nginx-aiops.conf`](../deploy/nginx-aiops.conf). Minimum:

```nginx
# once, in http {}
map $http_upgrade $connection_upgrade { default upgrade; '' close; }

server {
    listen 443 ssl;                      # a non-standard port such as 8443 is the same
    server_name monitor.example.com;
    client_max_body_size 100m;           # match the server body limit

    location / {
        proxy_pass http://127.0.0.1:8529;
        proxy_http_version 1.1;

        # address passthrough: drives install commands + write-request origin checks
        proxy_set_header Host              $http_host;   # $http_host keeps the port, $host drops it
        proxy_set_header X-Forwarded-Host  $http_host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Port  $server_port; # required on non-standard ports
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;

        # required for terminal / desktop / auto-update — all four
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_buffering         off;
        proxy_request_buffering off;     # without this, agent auto-update fails forever
        proxy_read_timeout  3600s;
        proxy_send_timeout  3600s;
    }
}
```

| Missing | Symptom |
|---|---|
| `proxy_request_buffering off` | the agent's upstream is held until the command ends → **auto-update / playbooks fail as "agent did not pick up"**, terminal never connects |
| `proxy_buffering off` | terminal/desktop output stutters; SSE AI answers arrive only at the end |
| default `proxy_read_timeout 60s` | terminal sessions and long polls are cut periodically |
| `Upgrade` / `Connection` | terminal WebSocket cannot connect |
| `Host $host` (port dropped) or no `X-Forwarded-Host` | writes fail with a 403 origin check; install command points at the wrong port |
| `X-Forwarded-Port` | on `:8443`-style ports the generated `SERVER=` loses the port and agents cannot register |

> `X-Real-IP` / `X-Forwarded-For` are ignored by default (anti-spoofing). Set
> `"trust_proxy": true` in `server_config.json` once the panel is only reachable through a trusted
> proxy. Fallback for any address confusion: pin `public_url` (or `AIOPS_PUBLIC_URL`).
>
> Self-check: when the proxy really is buffering the upstream, the server detects it, writes the
> exact directives to add into the system log, and still finishes that upgrade/playbook run.

Details: [INSTALL_EN.md](INSTALL_EN.md) · [DEPLOY_GUIDE_EN.md](DEPLOY_GUIDE_EN.md)

---

## Recommended path

1. **Enroll hosts** → generate install command → run on targets → confirm online  
2. **Watch metrics** → host detail trends; add probes / API monitors later  
3. **Govern alerts** → thresholds + silence / inhibit / route → IM / email  
4. **Troubleshoot remotely** → Web terminal; Windows desktop needs service-installed agent  
5. **Expand** → playbooks, SRE loop, AI / MCP, security center, SNMP, …  

Classic UI: `/` · Vue console: `/v2/` or `/?ui=v2`

---

## Configuration

### Server (env)

| Variable | Purpose | Required |
|---|---|---|
| `AIOPS_POSTGRES_DSN` | PostgreSQL | yes |
| `AIOPS_VM_URL` | VictoriaMetrics | yes |
| `AIOPS_LISTEN` | Listen addr (default `:8529`) | no |
| `AIOPS_SECRET_KEY` | AES-GCM config encryption | recommended |
| `AIOPS_TLS_CERT` / `AIOPS_TLS_KEY` | HTTPS | recommended |
| `AIOPS_TRUST_PROXY` | Trust reverse-proxy headers | no |

### Agent (`config.yaml`)

Key groups: `server` / `token` / `category`, report intervals, `servers[]` fan-out, logs, relay, Redfish / SNMP / NetFlow, etc.  
Sample: [config.example.yaml](config.example.yaml)

---

## Architecture

```
Browser / mobile* ──REST/WS──► Go server ──► PostgreSQL
                                  │           VictoriaMetrics
                                  ▲
                     reverse connect / report
                                  │
                            Go agent (metrics + terminal/desktop tunnel)
```

- Agent dials out; browser uses REST / WebSocket  
- Both stores are mandatory — missing either refuses boot  
- Same agent surface extends to out-of-band collectors and plugins  

---

## Docs & boundaries

| Doc | What |
|---|---|
| [USER_GUIDE.md](USER_GUIDE.md) | Scenarios |
| [INSTALL_EN.md](INSTALL_EN.md) / [DEPLOY_GUIDE_EN.md](DEPLOY_GUIDE_EN.md) | Install & advanced deploy |
| [FORWARD_GUIDE.md](FORWARD_GUIDE.md) | Port forwarding |
| [docs/ci-gate.md](docs/ci-gate.md) | CI / SQL / closed-loop gates |
| [docs/year1-acceptance.md](docs/year1-acceptance.md) | POC checklist |

**Boundaries**

- Single-instance scale has limits; large fleets need capacity planning  
- Without an LLM, AI falls back to heuristics  
- Windows lock-screen desktop needs a **service-installed** agent  
- Mobile packages are external to this repo  

Optional paid services (deploy consulting, SSO, private mobile signing, etc.) sit on top of the AGPL-3.0 core — open an Issue to discuss.

---

## Contributing

Issues / PRs / docs / translations welcome. Because the platform is wide, **start with the primary path** (easier to review and verify):

1. Agent install / report / reverse channel reliability  
2. Host monitoring & alert governance UX  
3. Web terminal / desktop and install docs  

Dev: Go 1.26+; `make build` · `make audit`.  
Please report security issues privately, not in public Issues.

| Resource | Link |
|---|---|
| GitHub | <https://github.com/sreyun/aiops> |
| Gitee | <https://gitee.com/bigdatasafe/aiops> |
| Releases | <https://github.com/sreyun/aiops/releases> |

---

## License

**AGPL-3.0** — see [LICENSE](LICENSE). No host caps, no feature gating, no forced telemetry.  
Third-party code under `vendor/` keeps its own licenses. Mobile clients are separate distributions.

---

<p align="center">
  <b>First: install agent → see host → open terminal.<br/>Everything else lives in the same platform you fully control.</b>
</p>
