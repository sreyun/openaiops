<div align="center">

# AIOps

**Console d’exploitation auto-hébergée sans ports entrants : voir les hôtes · ouvrir terminal/bureau · maîtriser les alertes**

[![Version](https://img.shields.io/badge/Version-v1.0.6-blue)](https://github.com/sreyun/aiops/releases/tag/v1.0.6)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS-lightgrey)]()

**[简体中文](README.md) · [繁體中文](README.zh-TW.md) · [English](README_EN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt-BR.md) · [Русский](README.ru.md)**

</div>

> Beaucoup de machines sont derrière NAT/pare-feu : l’agent s’installe, les ports entrants non.  
> AIOps utilise un **agent en connexion sortante** pour regrouper monitoring, terminal/bureau web et alertes dans un plan de contrôle auto-hébergé.

**Version [v1.0.6](https://github.com/sreyun/aiops/releases/tag/v1.0.6)** · [GitHub](https://github.com/sreyun/aiops) / [Gitee](https://gitee.com/bigdatasafe/aiops) · [CHANGELOG](CHANGELOG.md)

> Docs d’installation détaillées : **[English](INSTALL_EN.md)** / [中文](INSTALL.md).

---

## Commencer ici (~3 minutes)

```bash
docker compose up -d
open http://localhost:8529
# Copier la commande d’install depuis l’UI et l’exécuter sur l’hôte cible
# curl -fsSL "http://<server>:8529/install.sh?token=<TOKEN>" | sudo sh
```

Vérifiez ensuite :

1. **Hôte en ligne** avec métriques CPU/RAM/disque  
2. **Terminal web** ouvre un shell  
3. **Une alerte de seuil** est bien livrée  

C’est le parcours prioritaire : **console ops en reverse-connect**. Le reste s’appuie dessus.

---

## Pourquoi AIOps

| | |
|---|---|
| **Reverse connect** | L’agent sort ; terminal, bureau et forwards partagent le tunnel |
| **Binaire unique + agent sans deps** | Un serveur Go ; agent stdlib Linux/Windows/macOS/Kylin |
| **Vos données chez vous** | PostgreSQL + VictoriaMetrics, MIT, sans gates |

> La plateforme peut être large — **la porte d’entrée reste étroite.**

---

## Carte des capacités

```
Parcours principal
  Métriques hôtes → gouvernance d’alertes → terminal/bureau web → port forward

Extensions
  Probes / API · logs · playbooks · SRE · AI / MCP · sécurité
  Hyper-V / conteneurs / K8s · SNMP / NetFlow / Redfish · SQL · mobile*
```

<details>
<summary><b>Hôtes & ressources</b></summary>

- Métriques natives (GPU inclus) ; hors-bande (Redfish, NetFlow, SNMP, inventaires)  
- Recherche globale et aides topologiques  

</details>

<details>
<summary><b>Alertes & observabilité</b></summary>

- Seuils + silence/inhibit/route ; Feishu/DingTalk/e-mail/SMS/voix  
- Probes Ping/TCP/HTTP/processus ; dispo API / P95  
- Collecte de logs + recherche ; séries dans VictoriaMetrics  

</details>

<details>
<summary><b>Terminal, bureau & forward</b></summary>

- Terminal web (replay, audit, 2e mot de passe)  
- Bureau web JPEG/H.264 ; écran de verrouillage Windows = **installation service**  
- Port forward / proxy HTTP (WebSocket), protection SSRF  

</details>

<details>
<summary><b>Automation, SRE & AI</b></summary>

- Playbooks, remédiation contrôlée, incidents/SLO/tickets  
- Inspection AI / RCA ; RAG pgvector ; Hermes ; MCP pour Cursor/Claude  

</details>

---

## Installation

```bash
docker compose up -d
```

Ouvrez `http://localhost:8529`, terminez l’init sécurité, activez la MFA.  
Détails : [INSTALL_EN.md](INSTALL_EN.md).

---

## Parcours recommandé

1. Enrôler les hôtes → vérifier online  
2. Voir les métriques → probes optionnels  
3. Gouverner les alertes → IM/e-mail  
4. Dépanner à distance → terminal ; bureau Windows en service  
5. Étendre → playbooks, SRE, AI/MCP, sécurité  

UI classique : `/` · Vue : `/v2/`

---

## Architecture

```
Navigateur/mobile* ──REST/WS──► Serveur Go ──► PostgreSQL + VictoriaMetrics
                                    ▲
                           Connexion sortante
                                    │
                               Agent Go
```

Les deux stores sont obligatoires. Licence **MIT** : [LICENSE](LICENSE).  
Contributions bienvenues sur le parcours principal. Repo : <https://github.com/sreyun/aiops>

---

<p align="center"><b>D’abord : installer l’agent → voir l’hôte → ouvrir le terminal.</b></p>
