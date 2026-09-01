<div align="center">

# AIOps

**Plateforme open-source auto-hébergée de supervision d’hôtes & SRE**  
Observer · Alerter · Remédier · Ops à distance · Agent OTA · Diagnostic IA — un binaire sous votre contrôle.

[![Version](https://img.shields.io/badge/Version-v0.20.49-blue)](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](../../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Android%20%7C%20HarmonyOS-lightgrey)]()
[![Stars](https://img.shields.io/github/stars/sreyun/openaiops?style=social)](https://github.com/sreyun/openaiops)

**[简体中文](../../README.md) · [繁體中文](zh-TW.md) · [English](en.md) · [日本語](ja.md) · [한국어](ko.md) · [Français](fr.md) · [Deutsch](de.md) · [Español](es.md) · [Português](pt-BR.md) · [Русский](ru.md)**

[Démarrage rapide](#-démarrage-rapide) · [Capacités clés](#-capacités-clés) · [Documentation](../README.md) · [Journal des changements](../../CHANGELOG.md) · [Releases](https://github.com/sreyun/openaiops/releases)

</div>

---

## Pourquoi AIOps

Les piles ops s’empilent : métriques, alertes, bastion, runbooks… Les suites commerciales facturent à l’hôte et gardent vos données dans leur cloud.

AIOps regroupe le chemin courant en **une plateforme auto-hébergée** :

| | AIOps | Stack collée typique |
|---|---|---|
| **Pièces** | 1 serveur Go + 1 agent sans dépendance | Zabbix / Prometheus / Grafana / Alertmanager / bastion / runbooks… |
| **Mise en service** | `docker compose up -d` (~3 min) | Des jours d’intégration |
| **Données** | PostgreSQL + VictoriaMetrics, **à vous** | SaaS ou bases dispersées |
| **Distant** | Terminal / bureau / port-forward web ; agent **sortant uniquement** | VPN / bastion en plus |
| **Flotte** | **Mise à jour OTA Agent** (SHA-256, fenêtre de maintenance, push groupé, rollback) | Remplacement SSH hôte par hôte |
| **Boucle** | Alerte → playbook → incident/SLO/ticket → RCA IA | Collé à la main |
| **Licence** | **AGPL-3.0**, pas de plafond d’hôtes | Facturation par nœud / module |

> Pour DC privés, cloud hybride, et équipes qui veulent visibilité, contrôle, sûreté du changement et ops explicables.

---

## ✨ Capacités clés

Sept piliers — pas une liste à lessive :

```
  Observe ──────► Govern ──────► Remediate ──────► Diagnose
  Hosts/GPU/logs   Silence/route   Playbooks/gates   AI · RAG · MCP
  Probes/OOB       Multi-channel   Incident/SLO      Evidence gate

  Remote · terminal/desktop/forward (reverse tunnel)   Fleet · Agent OTA
  Security · RBAC/MFA/FIM
```

1. **Observer** — Agent multi-OS (Linux / Windows / macOS / Kylin), GPU, logs, sondes HTTP/TCP, SLI API, Redfish / SNMP / NetFlow / conteneurs / K8s / Hyper-V.
2. **Gouverner** — Seuils, silence / inhibit / route ; Feishu / DingTalk / e-mail / SMS / voix.
3. **Remédier & SRE** — Playbooks avec garde-fous d’approbation ; incidents, SLO, tickets, fenêtres de gel, break-glass audité.
4. **Diagnostic IA** — Inspection + RCA (modèles compatibles OpenAI ; heuristiques sinon) ; RAG pgvector, Skills, MCP (Cursor / Claude) ; auto-test vocal.
5. **Ops à distance** — Terminal web (replay, observation, audit, mot de passe secondaire), bureau distant (JPEG/H.264), port-forward / proxy HTTP avec garde SSRF.
6. **Livraison sécurisée** — RBAC, MFA, empreinte agent, crypto AES-256-GCM ; console Web ; apps Android / HarmonyOS séparées.
7. **Agent OTA** — Après mise à jour serveur, agents en ligne en retard auto-enfilés (ON par défaut) ; push groupé console ou `POST /api/v1/agents/update` ; téléchargement `/dl/` vérifié SHA-256, rollback `.bak`.

Version actuelle **[v0.20.49](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)** · Miroirs : [GitHub](https://github.com/sreyun/openaiops) / [Gitee](https://gitee.com/bigdatasafe/openaiops)

---

## 🚀 Démarrage rapide

> Le serveur **exige** PostgreSQL et VictoriaMetrics.

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

Installation → **[../getting-started/install.en.md](../getting-started/install.en.md)** · Production → **[../getting-started/deploy.en.md](../getting-started/deploy.en.md)**

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

## 📚 Documentation

Les docs longues et README localisés sont sous [`docs/`](../README.md). La racine ne garde que le README chinois et le changelog.

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

## 🤝 Contribution

Issues, PR et traductions bienvenues. Suggéré : `make build` · `make audit`.

Si AIOps remplace une stack collée pour vous, **mettez une Star** — cela aide la visibilité et la maintenance.

---

## Licence

[AGPL-3.0](../../LICENSE). Pas de plafond d’hôtes. Clients mobiles en paquets séparés (sources hors de ce dépôt).

---

<p align="center">
  <b>AIOps · Réduire la complexité ops dans une plateforme que vous possédez.</b><br/>
  <sub>Star ⭐ · Fork · Issue · Construisons l’ops auto-hébergée ensemble</sub>
</p>
