<div align="center">

# AIOps

**Open-source, self-hosted host monitoring & SRE platform**  
Observe · Alert · Remediate · Remote ops · Agent OTA · AI diagnosis — one binary you fully control.

[![Version](https://img.shields.io/badge/Version-v0.20.49-blue)](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](../../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Android%20%7C%20HarmonyOS-lightgrey)]()
[![Stars](https://img.shields.io/github/stars/sreyun/openaiops?style=social)](https://github.com/sreyun/openaiops)

**[简体中文](../../README.md) · [繁體中文](zh-TW.md) · [English](en.md) · [日本語](ja.md) · [한국어](ko.md) · [Français](fr.md) · [Deutsch](de.md) · [Español](es.md) · [Português](pt-BR.md) · [Русский](ru.md)**

[Quick start](#-quick-start) · [Core capabilities](#-core-capabilities) · [Docs](../README.md) · [Changelog](../../CHANGELOG.md) · [Releases](https://github.com/sreyun/openaiops/releases)

</div>

---

## Why AIOps

Ops stacks keep growing: metrics here, alerts there, a bastion for shells, another tool for runbooks. Commercial suites meter by host or module — and keep your data in their cloud.

AIOps collapses the common path into **one self-hosted platform**:

| | AIOps | Typical glue stack |
|---|---|---|
| **Parts** | 1 Go server + 1 zero-dep agent | Zabbix / Prometheus / Grafana / Alertmanager / bastion / runbooks… |
| **Time-to-value** | `docker compose up -d` (~3 min) | Days of wiring |
| **Data** | PostgreSQL + VictoriaMetrics, **yours** | SaaS or scattered DBs |
| **Remote** | Web terminal / desktop / port-forward; agent **outbound-only** | Extra VPN / bastion |
| **Fleet** | **Agent OTA auto-update** (SHA-256, maintenance window, batch push, rollback) | Per-host SSH binary swap |
| **Loop** | Alert → playbook → incident/SLO/ticket → AI RCA | Humans glue the gaps |
| **License** | **AGPL-3.0**, no host caps | Per-node / per-module fees |

> Built for private DC, hybrid cloud, and teams that need visibility, control, change safety, and explainable ops.

---

## ✨ Core capabilities

Seven pillars — not a laundry list:

```
  Observe ──────► Govern ──────► Remediate ──────► Diagnose
  Hosts/GPU/logs   Silence/route   Playbooks/gates   AI · RAG · MCP
  Probes/OOB       Multi-channel   Incident/SLO      Evidence gate

  Remote · terminal/desktop/forward (reverse tunnel)   Fleet · Agent OTA
  Security · RBAC/MFA/FIM
```

1. **Observe** — Cross-platform agent (Linux / Windows / macOS / Kylin), GPU, logs, HTTP/TCP probes, API SLIs, Redfish / SNMP / NetFlow / containers / K8s / Hyper-V.  
2. **Govern** — Threshold presets, silence / inhibit / route; Feishu / DingTalk / email / SMS / voice.  
3. **Remediate & SRE** — Playbooks with approval guardrails; incidents, SLO, tickets, freeze windows, audited break-glass.  
4. **AI diagnosis** — Inspection + RCA (OpenAI-compatible models; heuristics if unset); pgvector RAG, Skills, MCP for Cursor / Claude; speech self-test.  
5. **Remote ops** — Web terminal (replay, observe, audit, secondary password), remote desktop (JPEG/H.264), port-forward / HTTP proxy with SSRF guards.  
6. **Secure delivery** — RBAC, MFA, agent fingerprint, AES-256-GCM config crypto; Web console; Android / HarmonyOS apps distributed separately.  
7. **Agent OTA** — After a server upgrade, lagging online agents auto-enqueue (default on); batch push from the console or `POST /api/v1/agents/update`; SHA-256 verified downloads from `/dl/` with `.bak` rollback; maintenance windows, exemptions, and skip-reason visibility.

Current release **[v0.20.49](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)** · Mirrors: [GitHub](https://github.com/sreyun/openaiops) / [Gitee](https://gitee.com/bigdatasafe/openaiops)

---

## 🚀 Quick start

> Server **requires** both PostgreSQL and VictoriaMetrics.

```bash
bash scripts/secure-compose.sh   # generate .env secrets
docker compose up -d
# open http://localhost:8529 → admin / admin (forced password change on first login)
# install agents from the UI; after server upgrades agents OTA by default
```

Binary / from source:

```bash
export AIOPS_POSTGRES_DSN="postgres://aiops:secret@127.0.0.1:5432/aiops?sslmode=disable"
export AIOPS_VM_URL="http://127.0.0.1:8428"
./aiops-server

go build ./cmd/server ./cmd/agent   # Go 1.26+
```

Full install → **[../getting-started/install.en.md](../getting-started/install.en.md)** · Production → **[../getting-started/deploy.en.md](../getting-started/deploy.en.md)**

---

## 🏗 Architecture

```mermaid
flowchart LR
  subgraph Clients
    Web[Web Console]
    Mob[Android / HarmonyOS]
  end
  subgraph Server
    API[HTTP · WS · MCP]
    Core[Alerts · Playbooks · SRE · AI]
    PG[(PostgreSQL)]
    VM[(VictoriaMetrics)]
  end
  subgraph Fleet
    Ag[Agents]
    Ext[BMC · switches · storage]
  end
  Web --> API
  Mob --> API
  API --> Core
  Core --> PG
  Core --> VM
  Ag -->|outbound report / terminal / OTA| API
  Ag --> Ext
```

---

## 📸 Product Screenshots

### Web Console

<table>
  <tr>
    <td align="center"><b>Overview Dashboard</b><br/><br/><img src="../image/1-shouye.png" alt="Overview Dashboard" width="100%"><br/>Unified view of cluster resources, alerts and activities: host online rate, system health status, active alerts overview; CPU / GPU / memory / disk / IO / IOPS resource TOP10 real-time ranking, locate bottleneck hosts at a glance.</td>
    <td align="center"><b>Host Management</b><br/><br/><img src="../image/2-zhuji.png" alt="Host Management" width="100%"><br/>Left asset tree grouped by datacenter / business, right card-style display shows real-time metrics for each host: CPU, memory, swap, disk partitions, 1/5/15 min load, network throughput, IOPS, process and connection count, supports grid / list dual view.</td>
  </tr>
  <tr>
    <td align="center"><b>Web Terminal</b><br/><br/><img src="../image/3-zhongduan.png" alt="Web Terminal" width="100%"><br/>Direct connection to target hosts via Agent reverse channel, no need to open SSH inbound ports. Supports multi-tab connections to multiple hosts, command audit and recording playback, observer mode.</td>
    <td align="center"><b>Remote Desktop</b><br/><br/><img src="../image/4-zhuomian.png" alt="Remote Desktop" width="100%"><br/>JPEG / H.264 dual-encoding remote desktop, supports multi-screen switching, adaptive resolution, system shortcuts like Ctrl+Alt+Del; right panel provides file upload/download and clipboard sync, operation experience close to local desktop.</td>
  </tr>
  <tr>
    <td align="center"><b>Agent Installation</b><br/><br/><img src="../image/5-agent.png" alt="Agent Installation" width="100%"><br/>One command to deploy Agent, supports Linux / Windows / macOS three platforms. Optional standard mode, gateway relay mode, multi-server push mode; Token strategy and auto-update strategy can be managed uniformly in the console.</td>
    <td align="center"><b>Hardware Resource Monitoring</b><br/><br/><img src="../image/6-jiqi.png" alt="Hardware Resource Monitoring" width="100%"><br/>Out-of-band collection of physical server hardware status via Redfish / BMC / iDRAC / iLO: vendor, model, serial number, power/temperature/power consumption, BIOS version; BMC event logs (SEL) retained completely, supports AI diagnosis.</td>
  </tr>
  <tr>
    <td align="center"><b>Container Management</b><br/><br/><img src="../image/7-docker.png" alt="Container Management" width="100%"><br/>Unified management of Docker / Podman containers and Compose projects on hosts: real-time status, port mapping, image information at a glance; supports one-click start/stop, restart, log viewing, cross-host batch filtering.</td>
    <td align="center"><b>Playbook Orchestration</b><br/><br/><img src="../image/8-juben.png" alt="Playbook Orchestration" width="100%"><br/>Visual automation operations playbooks: system inspection, network inspection, security inspection, systemd service restart, K8s Deployment rolling restart, deep host inspection, Java application inspection/performance analysis/exception analysis and other built-in playbooks ready to use, supports custom multi-step parallelism and approval guardrails.</td>
  </tr>
  <tr>
    <td align="center"><b>SRE Hub</b><br/><br/><img src="../image/9-sre.png" alt="SRE Hub" width="100%"><br/>Alert triggers / SLO burn-down / manually created events converge here, with complete timeline and auto-remediation records. Supports eight sub-modules: incidents, auto-remediation, dependency topology, SLO, tickets, On-call, changes, platform health inspection.</td>
    <td align="center"><b>AI Diagnosis</b><br/><br/><img src="../image/10-ai.png" alt="AI Diagnosis" width="100%"><br/>One-click AI assistant in SRE event list, automatically analyzes current alert root cause and gives disposal suggestions. AI reviews alert correlations, retrieves similar cases, checks critical host health status, thinking process fully visible.</td>
  </tr>
  <tr>
    <td align="center"><b>Alert Settings</b><br/><br/><img src="../image/11-setting.png" alt="Alert Settings" width="100%"><br/>Multi-channel alert push configuration: Feishu, DingTalk, Webhook, email, SMS, phone six channels optional; supports silence / inhibit / routing policies, critical goes to phone SMS, warnings go to IM, avoids alert storms.</td>
    <td align="center"><b>AI Settings</b><br/><br/><img src="../image/12-aiset.png" alt="AI Settings" width="100%"><br/>One-stop AI capability configuration: dialogue models (OpenAI compatible / Bailian / DeepSeek / Ollama / Anthropic / Claude), RAG vector library, judgment and cost (MoA / unit price), MCP integration, call observation, security authorization six settings, supports voice input/broadcast.</td>
  </tr>
</table>

### Mobile App (Android / HarmonyOS)

> **Note**: Mobile App (Android / HarmonyOS) is a separate distribution package, **the open-source community version does not provide App installation packages**. If you need to use the mobile end, please contact the project team.

<table>
  <tr>
    <td align="center"><b>SRE Cockpit</b><br/><br/><img src="../image/app01.jpg" alt="SRE Cockpit" width="100%"><br/>Mobile overview page: host online rate, severe/warning alert counts at a glance; quick entries cover hardware monitoring, virtual machines, network traffic, dial testing, host monitoring, log retrieval, operations orchestration, dashboards; pending incidents sorted by priority.</td>
    <td align="center"><b>Infrastructure Monitoring</b><br/><br/><img src="../image/app02.jpg" alt="Infrastructure Monitoring" width="100%"><br/>Mobile infrastructure page: four dimensions of host/resource/network/dial testing switching; GPU resource overview (model, VRAM, temperature); host list filtered by group, real-time display of CPU, memory, disk and other core metrics.</td>
  </tr>
  <tr>
    <td align="center"><b>Mobile Terminal</b><br/><br/><img src="../image/app03.jpg" alt="Mobile Terminal" width="100%"><br/>Mobile web terminal: direct connection to target hosts via Agent reverse channel, complete terminal interactive experience; supports shortcut keys, font scaling, screen rotation, troubleshoot anytime anywhere.</td>
    <td align="center"><b>AI Operations Assistant</b><br/><br/><img src="../image/app04.jpg" alt="AI Operations Assistant" width="100%"><br/>Mobile AI dialogue: describe problems in natural language, AI automatically retrieves historical cases, pulls alert details, checks host health status, gives root cause analysis and disposal suggestions; bottom navigation bar covers overview/monitoring/alerts/operations/AI five major entries.</td>
  </tr>
</table>

---

## 📚 Documentation

Long-form docs live under [`docs/`](../README.md). The repo root keeps only the Chinese README and changelog.

| Need | Doc |
|------|-----|
| Install | [../getting-started/install.md](../getting-started/install.md) · [EN](../getting-started/install.en.md) |
| Production deploy | [../getting-started/deploy.md](../getting-started/deploy.md) · [EN](../getting-started/deploy.en.md) |
| Agent OTA soak checklist | [../engineering/agent-update-soak.md](../engineering/agent-update-soak.md) |
| End-user guide | [../guides/user-guide.md](../guides/user-guide.md) |
| Port forward | [../guides/forward.md](../guides/forward.md) |
| Content audit / playbooks | [../guides/content-audit.md](../guides/content-audit.md) |
| CI / SQL gates | [../engineering/ci-gate.md](../engineering/ci-gate.md) |

---

## 🤝 Contributing

Issues, PRs, and translations welcome. Suggested: `make build` · `make audit`.

If AIOps replaces a glue stack for you, **please Star the repo** — it keeps the project visible and maintainable.

---

## License

[AGPL-3.0](../../LICENSE). No host caps, no “enterprise-only” traps. Mobile clients are separate packages (source not in this repo).

---

<p align="center">
  <b>AIOps · Collapse ops complexity into a platform you own.</b><br/>
  <sub>Star ⭐ · Fork · Open an issue · Build self-hosted ops together</sub>
</p>
