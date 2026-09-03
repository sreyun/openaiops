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

## 📸 Captures d'écran produit

### Console Web

<table>
  <tr>
    <td align="center"><b>Tableau de bord</b><br/><br/><img src="../../image/1-shouye.png" alt="Tableau de bord" width="100%"><br/>Vue unifiée des ressources cluster, alertes et activités : taux de disponibilité des hôtes, état de santé du système, aperçu des alertes actives ; TOP10 des ressources CPU / GPU / mémoire / disque / IO / IOPS en temps réel, localisez les goulots d'étranglement en un coup d'œil.</td>
    <td align="center"><b>Gestion des hôtes</b><br/><br/><img src="../../image/2-zhuji.png" alt="Gestion des hôtes" width="100%"><br/>Arborescence des actifs à gauche groupée par datacenter / métier, affichage en cartes à droite avec les métriques temps réel de chaque hôte : CPU, mémoire, swap, partitions disque, charge 1/5/15 min, débit réseau, IOPS, processus et connexions, vue grille / liste.</td>
  </tr>
  <tr>
    <td align="center"><b>Terminal Web</b><br/><br/><img src="../../image/3-zhongduan.png" alt="Terminal Web" width="100%"><br/>Connexion directe aux hôtes cibles via le canal inverse de l'Agent, sans ouvrir de port SSH entrant. Multi-onglets pour plusieurs hôtes, audit des commandes et lecture d'enregistrements, mode observateur.</td>
    <td align="center"><b>Bureau à distance</b><br/><br/><img src="../../image/4-zhuomian.png" alt="Bureau à distance" width="100%"><br/>Bureau distant à double encodage JPEG / H.264, changement multi-écrans, résolution adaptative, raccourcis système Ctrl+Alt+Del ; panneau droit pour upload/download de fichiers et synchronisation du presse-papiers, expérience proche du bureau local.</td>
  </tr>
  <tr>
    <td align="center"><b>Installation Agent</b><br/><br/><img src="../../image/5-agent.png" alt="Installation Agent" width="100%"><br/>Une commande pour déployer l'Agent, supporte Linux / Windows / macOS. Mode standard, relais passerelle, push multi-serveur ; stratégie Token et mise à jour auto gérées depuis la console.</td>
    <td align="center"><b>Surveillance matérielle</b><br/><br/><img src="../../image/6-jiqi.png" alt="Surveillance matérielle" width="100%"><br/>Collecte hors bande de l'état matériel des serveurs physiques via Redfish / BMC / iDRAC / iLO : fabricant, modèle, numéro de série, alimentation/température/consommation, version BIOS ; journaux d'événements BMC (SEL) conservés, supporte le diagnostic IA.</td>
  </tr>
  <tr>
    <td align="center"><b>Gestion des conteneurs</b><br/><br/><img src="../../image/7-docker.png" alt="Gestion des conteneurs" width="100%"><br/>Gestion unifiée des conteneurs Docker / Podman et projets Compose sur les hôtes : état temps réel, mappage de ports, informations d'image ; démarrage/arrêt en un clic, redémarrage, visualisation des logs, filtrage multi-hôtes.</td>
    <td align="center"><b>Orchestration de Playbooks</b><br/><br/><img src="../../image/8-juben.png" alt="Orchestration de Playbooks" width="100%"><br/>Playbooks d'opérations automatisées visuels : inspection système, réseau, sécurité, redémarrage de services systemd, déploiement K8s rolling, inspection approfondie d'hôtes, inspection d'applications Java/analyse de performance/analyse d'exceptions ; playbooks intégrés prêts à l'emploi, parallélisation personnalisable et gardes-fous d'approbation.</td>
  </tr>
  <tr>
    <td align="center"><b>Centre SRE</b><br/><br/><img src="../../image/9-sre.png" alt="Centre SRE" width="100%"><br/>Les déclenchements d'alertes / burn-down SLO / événements créés manuellement convergent ici, avec timeline complète et enregistrements d'auto-remédiation. Huit sous-modules : incidents, auto-remédiation, topologie de dépendances, SLO, tickets, On-call, changements, inspection santé plateforme.</td>
    <td align="center"><b>Diagnostic IA</b><br/><br/><img src="../../image/10-ai.png" alt="Diagnostic IA" width="100%"><br/>Assistant IA en un clic depuis la liste d'événements SRE, analyse automatiquement la cause racine de l'alerte et donne des suggestions de traitement. L'IA corrèle les alertes, recherche des cas similaires, vérifie la santé des hôtes critiques, processus de réflexion entièrement visible.</td>
  </tr>
  <tr>
    <td align="center"><b>Paramètres d'alerte</b><br/><br/><img src="../../image/11-setting.png" alt="Paramètres d'alerte" width="100%"><br/>Configuration multi-canal de push d'alertes : Feishu, DingTalk, Webhook, e-mail, SMS, téléphone ; supports silence / inhibit / routing, les critiques vont au téléphone SMS, les avertissements à l'IM, évite les tempêtes d'alertes.</td>
    <td align="center"><b>Paramètres IA</b><br/><br/><img src="../../image/12-aiset.png" alt="Paramètres IA" width="100%"><br/>Configuration IA tout-en-un : modèles de dialogue (compatible OpenAI / Bailian / DeepSeek / Ollama / Anthropic / Claude), bibliothèque vectorielle RAG, jugement et coût (MoA / prix unitaire), intégration MCP, observation des appels, autorisation de sécurité ; supporte l'entrée vocale/la diffusion.</td>
  </tr>
</table>

### App Mobile (Android / HarmonyOS)

> **Note** : L'App Mobile (Android / HarmonyOS) est un package de distribution séparé, **la version communautaire open-source ne fournit pas de packages d'installation d'App**. Si vous avez besoin d'utiliser le mobile, veuillez contacter l'équipe du projet.

<table>
  <tr>
    <td align="center"><b>Cockpit SRE</b><br/><br/><img src="../../image/app01.jpg" alt="Cockpit SRE" width="100%"><br/>Page d'aperçu mobile : taux de disponibilité des hôtes, nombre d'alertes critiques/avertissements en un coup d'œil ; entrées rapides couvrant la surveillance matérielle, machines virtuelles, trafic réseau, tests de numérotation, surveillance d'hôtes, recherche de logs, orchestration d'opérations, tableaux de bord ; incidents en attente triés par priorité.</td>
    <td align="center"><b>Surveillance d'infrastructure</b><br/><br/><img src="../../image/app02.jpg" alt="Surveillance d'infrastructure" width="100%"><br/>Page d'infrastructure mobile : quatre dimensions hôte/ressource/réseau/test de numérotation ; aperçu des ressources GPU (modèle, VRAM, température) ; liste d'hôtes filtrée par groupe, affichage en temps réel des métriques clés CPU, mémoire, disque.</td>
  </tr>
  <tr>
    <td align="center"><b>Terminal Mobile</b><br/><br/><img src="../../image/app03.jpg" alt="Terminal Mobile" width="100%"><br/>Terminal web mobile : connexion directe aux hôtes cibles via le canal inverse de l'Agent, expérience interactive complète ; supporte les raccourcis, le zoom de police, la rotation d'écran, dépannage anytime anywhere.</td>
    <td align="center"><b>Assistant Ops IA</b><br/><br/><img src="../../image/app04.jpg" alt="Assistant Ops IA" width="100%"><br/>Dialogue IA mobile : décrivez les problèmes en langage naturel, l'IA recherche automatiquement les cas historiques, récupère les détails d'alertes, vérifie la santé des hôtes, donne l'analyse de cause racine et les suggestions de traitement ; la barre de navigation inférieure couvre les 5 entrées principales aperçu/surveillance/alertes/opérations/IA.</td>
  </tr>
</table>

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
