<div align="center">

# AIOps

**Plataforma open-source self-hosted de monitoramento de hosts e SRE**  
Observar · Alertar · Remediar · Ops remotas · Agent OTA · Diagnóstico de IA — um binário sob o seu controle.

[![Version](https://img.shields.io/badge/Version-v0.20.49-blue)](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](../../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Android%20%7C%20HarmonyOS-lightgrey)]()
[![Stars](https://img.shields.io/github/stars/sreyun/openaiops?style=social)](https://github.com/sreyun/openaiops)

**[简体中文](../../README.md) · [繁體中文](zh-TW.md) · [English](en.md) · [日本語](ja.md) · [한국어](ko.md) · [Français](fr.md) · [Deutsch](de.md) · [Español](es.md) · [Português](pt-BR.md) · [Русский](ru.md)**

[Início rápido](#-início-rápido) · [Capacidades principais](#-capacidades-principais) · [Documentação](../README.md) · [Changelog](../../CHANGELOG.md) · [Releases](https://github.com/sreyun/openaiops/releases)

</div>

---

## Por que AIOps

As pilhas de ops crescem: métricas, alertas, bastion e runbooks separados. Produtos comerciais cobram por host e mantêm seus dados na nuvem deles.

AIOps concentra o caminho comum em **uma plataforma self-hosted**:

| | AIOps | Stack típica “colada” |
|---|---|---|
| **Peças** | 1 servidor Go + 1 agente sem dependências | Zabbix / Prometheus / Grafana / Alertmanager / bastion / runbooks… |
| **Time-to-value** | `docker compose up -d` (~3 min) | Dias de integração |
| **Dados** | PostgreSQL + VictoriaMetrics, **seus** | SaaS ou BDs espalhados |
| **Remoto** | Terminal / desktop / port-forward web; agente **somente saída** | VPN / bastion extra |
| **Frota** | **OTA automático do Agent** (SHA-256, janela de manutenção, push em lote, rollback) | Troca SSH por host |
| **Loop** | Alerta → playbook → incidente/SLO/ticket → RCA IA | Pessoas colam as falhas |
| **Licença** | **AGPL-3.0**, sem limite de hosts | Por nó / módulo |

> Para DC privado, nuvem híbrida e times que precisam de visibilidade, controle, segurança de mudança e ops explicáveis.

---

## ✨ Capacidades principais

Sete pilares — não uma lista infinita :

```
  Observe ──────► Govern ──────► Remediate ──────► Diagnose
  Hosts/GPU/logs   Silence/route   Playbooks/gates   AI · RAG · MCP
  Probes/OOB       Multi-channel   Incident/SLO      Evidence gate

  Remote · terminal/desktop/forward (reverse tunnel)   Fleet · Agent OTA
  Security · RBAC/MFA/FIM
```

1. **Observar** — Agente multiplataforma (Linux / Windows / macOS / Kylin), GPU, logs, probes HTTP/TCP, SLIs de API, Redfish / SNMP / NetFlow / containers / K8s / Hyper-V.
2. **Governar** — Limiares, silence / inhibit / route; Feishu / DingTalk / e-mail / SMS / voz.
3. **Remediar e SRE** — Playbooks com aprovações; incidentes, SLO, tickets, janelas de freeze, break-glass auditado.
4. **Diagnóstico IA** — Inspeção + RCA (modelos compatíveis OpenAI; heurística se não houver); RAG pgvector, Skills, MCP (Cursor / Claude); autoteste de voz.
5. **Ops remotas** — Terminal web (replay, observação, auditoria, senha secundária), desktop remoto (JPEG/H.264), port-forward / proxy HTTP com proteção SSRF.
6. **Entrega segura** — RBAC, MFA, fingerprint do agente, AES-256-GCM; console Web; Android / HarmonyOS separados.
7. **Agent OTA** — Após upgrade do servidor, agents online atrasados entram na fila (ON por padrão); push em lote no console ou `POST /api/v1/agents/update`; download `/dl/` com SHA-256, rollback `.bak`.

Versão atual **[v0.20.49](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)** · Espelhos: [GitHub](https://github.com/sreyun/openaiops) / [Gitee](https://gitee.com/bigdatasafe/openaiops)

---

## 🚀 Início rápido

> O servidor **exige** PostgreSQL e VictoriaMetrics.

```bash
docker compose up -d
# open http://localhost:8529 → finish first-time security setup
# copy the Agent install command from the UI onto each host
```

```bash
export AIOPS_POSTGRES_DSN="postgres://aiops:secret@127.0.0.1:5432/aiops?sslmode=disable"
export AIOPS_VM_URL="http://127.0.0.1:8428"
./aiops-server

go build ./cmd/server ./cmd/agent   # Go 1.26+
```

Instalação → **[../getting-started/install.en.md](../getting-started/install.en.md)** · Produção → **[../getting-started/deploy.en.md](../getting-started/deploy.en.md)**

---

## 🏗 Arquitetura

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

## 📚 Documentação

Docs longas e READMEs localizados ficam em [`docs/`](../README.md). Na raiz ficam só o README chinês e o changelog.

| Need | Doc |
|------|-----|
| Install | [../getting-started/install.md](../getting-started/install.md) · [EN](../getting-started/install.en.md) |
| Agent OTA | [../engineering/agent-update-soak.md](../engineering/agent-update-soak.md) |
| Production deploy | [../getting-started/deploy.md](../getting-started/deploy.md) · [EN](../getting-started/deploy.en.md) |
| End-user guide | [../guides/user-guide.md](../guides/user-guide.md) |
| Port forward | [../guides/forward.md](../guides/forward.md) |
| Content audit / playbooks | [../guides/content-audit.md](../guides/content-audit.md) |
| CI / SQL gates | [../engineering/ci-gate.md](../engineering/ci-gate.md) |

---

## 🤝 Contribuir

Issues, PRs e traduções são bem-vindas. Sugestão: `make build` · `make audit`.

Se o AIOps substituir uma stack colada, **dê uma Star** — mantém o projeto visível e sustentável.

---

## Licença

[AGPL-3.0](../../LICENSE). Sem limite de hosts. Clientes móveis em pacotes separados (fonte fora deste repositório).

---

<p align="center">
  <b>AIOps · Reduza a complexidade de ops em uma plataforma que você possui.</b><br/>
  <sub>Star ⭐ · Fork · Issue · Vamos construir ops self-hosted juntos</sub>
</p>
