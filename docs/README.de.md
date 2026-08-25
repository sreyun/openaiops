<div align="center">

# AIOps

**Self-hosted Ops-Konsole ohne eingehende Ports: Hosts sehen · Terminal/Desktop öffnen · Alarme steuern**

[![Version](https://img.shields.io/badge/Version-v1.0.6-blue)](https://github.com/sreyun/aiops/releases/tag/v1.0.6)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS-lightgrey)]()

**[简体中文](README.md) · [繁體中文](README.zh-TW.md) · [English](README_EN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt-BR.md) · [Русский](README.ru.md)**

</div>

> Viele Maschinen liegen hinter NAT/Firewall — Agent ja, eingehende Ports oft nein.  
> AIOps nutzt einen **rückwärts verbindenden Agent**, der Monitoring, Web-Terminal/Desktop und Alerting in einer selbst gehosteten Control Plane vereint.

**Release [v1.0.6](https://github.com/sreyun/aiops/releases/tag/v1.0.6)** · [GitHub](https://github.com/sreyun/aiops) / [Gitee](https://gitee.com/bigdatasafe/aiops) · [CHANGELOG](CHANGELOG.md)

> Ausführliche Installationsdocs: **[English](INSTALL_EN.md)** / [中文](INSTALL.md). Diese Seite ist die deutsche Einstiegs-Story.

---

## Zuerst dieser Pfad (~3 Minuten)

```bash
docker compose up -d
open http://localhost:8529
# Installationsbefehl aus der UI auf dem Zielhost ausführen (Agent verbindet ausgehend)
# curl -fsSL "http://<server>:8529/install.sh?token=<TOKEN>" | sudo sh
```

Danach prüfen:

1. **Host online** mit CPU-/RAM-/Disk-Metriken  
2. **Web-Terminal öffnen**  
3. **Einen Schwellwert-Alarm** setzen und Zustellung prüfen  

Das ist der Fokus: **Reverse-Connect Ops-Konsole**. Alles andere baut darauf auf.

---

## Warum AIOps

| | |
|---|---|
| **Reverse Connect** | Agent wählt aus; Terminal, Desktop und Forwards teilen denselben Tunnel |
| **Ein Binary + Zero-Dep Agent** | Ein Go-Server; stdlib-Agent auf Linux/Windows/macOS/Kylin |
| **Daten bei Ihnen** | PostgreSQL + VictoriaMetrics, vollständig selbst gehostet, keine Telemetriepflicht |

> Die Plattform darf breit sein — **die Haustür bleibt schmal.**

---

## Fähigkeitskarte

```
Primärpfad
  Host-Metriken → Alert-Governance → Web-Terminal/Desktop → Port-Forward

Erweiterungen
  Probes / API · Logs · Playbooks · SRE · AI / MCP · Security
  Hyper-V / Container / K8s · SNMP / NetFlow / Redfish · SQL · Mobile*
```

<details>
<summary><b>Hosts & Ressourcen</b></summary>

- Native Metriken inkl. GPU; Out-of-Band (Redfish, NetFlow, SNMP, Inventare)  
- Globale Ressourcensuche und Topologie-Helfer  

</details>

<details>
<summary><b>Alerts & Observability</b></summary>

- Schwellen + Silence/Inhibit/Route; Feishu/DingTalk/E-Mail/SMS/Voice  
- Probes: Ping/TCP/HTTP/Prozess; API-Verfügbarkeit/P95  
- Log-Tail + Suche; Zeitreihen in VictoriaMetrics  

</details>

<details>
<summary><b>Terminal, Desktop & Forward</b></summary>

- Web-Terminal mit Replay/Audit/Zweitpasswort  
- Web-Desktop (JPEG/H.264); Windows-Lockscreen braucht **Service-Installation**  
- Port-Forward / HTTP-Proxy inkl. WebSocket, SSRF-Schutz  

</details>

<details>
<summary><b>Automation, SRE & AI</b></summary>

- Playbooks, gegatete Auto-Remediation, Incidents/SLO/Tickets  
- AI-Inspektion/RCA; pgvector-RAG; Hermes; MCP für Cursor/Claude  

</details>

---

## Installation

```bash
docker compose up -d
```

UI: `http://localhost:8529` — Security-Init erzwingen, MFA empfohlen.  
Details: [INSTALL_EN.md](INSTALL_EN.md).

---

## Empfohlener Weg

1. Hosts enrollen → online prüfen  
2. Metriken ansehen → optional Probes  
3. Alerts regeln → IM/E-Mail  
4. Remote troubleshooting → Terminal; Windows-Desktop als Service  
5. Erweitern → Playbooks, SRE, AI/MCP, Security  

Classic UI: `/` · Vue: `/v2/`

---

## Architektur

```
Browser/Mobile* ──REST/WS──► Go-Server ──► PostgreSQL + VictoriaMetrics
                                 ▲
                        Reverse Connect
                                 │
                            Go-Agent
```

Beide Stores sind Pflicht. **AGPL-3.0**: [LICENSE](../LICENSE) — kostenlos und ohne Host-Limit für den internen Eigenbetrieb; für Closed-Source-Vertrieb, Einbettung oder als Netzwerkdienst ist eine **kommerzielle Lizenz** nötig: [LICENSING.md](../LICENSING.md).  
Beiträge willkommen — idealerweise am Primärpfad (Agent/Monitoring/Terminal).  
Repos: <https://github.com/sreyun/aiops>

---

<p align="center"><b>Zuerst: Agent installieren → Host sehen → Terminal öffnen.</b></p>
