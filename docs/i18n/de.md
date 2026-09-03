<div align="center">

# AIOps

**Open-Source, selbst gehostete Host-Monitoring- & SRE-Plattform**  
Beobachten · Alarmieren · Beheben · Remote-Ops · Agent OTA · KI-Diagnose — eine Binary unter Ihrer Kontrolle.

[![Version](https://img.shields.io/badge/Version-v0.20.49-blue)](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](../../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Android%20%7C%20HarmonyOS-lightgrey)]()
[![Stars](https://img.shields.io/github/stars/sreyun/openaiops?style=social)](https://github.com/sreyun/openaiops)

**[简体中文](../../README.md) · [繁體中文](zh-TW.md) · [English](en.md) · [日本語](ja.md) · [한국어](ko.md) · [Français](fr.md) · [Deutsch](de.md) · [Español](es.md) · [Português](pt-BR.md) · [Русский](ru.md)**

[Schnellstart](#-schnellstart) · [Kernfähigkeiten](#-kernfähigkeiten) · [Dokumentation](../README.md) · [Änderungsprotokoll](../../CHANGELOG.md) · [Releases](https://github.com/sreyun/openaiops/releases)

</div>

---

## Warum AIOps

Ops-Stacks wachsen: Metriken hier, Alarme dort, Bastion und Runbooks woanders. Kommerzielle Suiten rechnen nach Host ab — und behalten Ihre Daten in ihrer Cloud.

AIOps bündelt den üblichen Pfad in **eine selbst gehostete Plattform**:

| | AIOps | Typischer Klebe-Stack |
|---|---|---|
| **Teile** | 1 Go-Server + 1 agent ohne Dependencies | Zabbix / Prometheus / Grafana / Alertmanager / Bastion / Runbooks… |
| **Time-to-Value** | `docker compose up -d` (~3 Min.) | Tage an Verdrahtung |
| **Daten** | PostgreSQL + VictoriaMetrics, **Ihnen** | SaaS oder verstreute DBs |
| **Remote** | Web-Terminal / Desktop / Port-Forward; Agent nur **ausgehend** | Extra-VPN / Bastion |
| **Flotte** | **Agent-OTA-Auto-Update** (SHA-256, Wartungsfenster, Batch-Push, Rollback) | SSH-Binary pro Host |
| **Schleife** | Alarm → Playbook → Incident/SLO/Ticket → KI-RCA | Menschen kleben Lücken |
| **Lizenz** | **AGPL-3.0**, kein Host-Cap | Pro Node / Modul |

> Für private Rechenzentren, Hybrid-Cloud und Teams, die Sichtbarkeit, Kontrolle, Änderungssicherheit und erklärbare Ops brauchen.

---

## ✨ Kernfähigkeiten

Sieben Säulen — keine Feature-Wäscheliste :

```
  Observe ──────► Govern ──────► Remediate ──────► Diagnose
  Hosts/GPU/logs   Silence/route   Playbooks/gates   AI · RAG · MCP
  Probes/OOB       Multi-channel   Incident/SLO      Evidence gate

  Remote · terminal/desktop/forward (reverse tunnel)   Fleet · Agent OTA
  Security · RBAC/MFA/FIM
```

1. **Beobachten** — Plattformübergreifender Agent (Linux / Windows / macOS / Kylin), GPU, Logs, HTTP/TCP-Probes, API-SLIs, Redfish / SNMP / NetFlow / Container / K8s / Hyper-V.
2. **Steuern** — Schwellwert-Presets, Silence / Inhibit / Route; Feishu / DingTalk / E-Mail / SMS / Sprache.
3. **Beheben & SRE** — Playbooks mit Freigabe-Guardrails; Incidents, SLO, Tickets, Freeze-Fenster, auditiertes Break-Glass.
4. **KI-Diagnose** — Inspektion + RCA (OpenAI-kompatibel; sonst Heuristik); pgvector-RAG, Skills, MCP (Cursor / Claude); Sprach-Selbsttest.
5. **Remote-Ops** — Web-Terminal (Replay, Beobachten, Audit, Zweitpasswort), Remote-Desktop (JPEG/H.264), Port-Forward / HTTP-Proxy mit SSRF-Schutz.
6. **Sichere Auslieferung** — RBAC, MFA, Agent-Fingerprint, AES-256-GCM; Web-Konsole; Android / HarmonyOS separat.
7. **Agent OTA** — Nach Server-Upgrade hängen online Agents automatisch in der Queue (Standard AN); Batch-Push in der Konsole oder `POST /api/v1/agents/update`; SHA-256-geprüfter Download von `/dl/`, `.bak`-Rollback.

Aktuelles Release **[v0.20.49](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)** · Spiegel: [GitHub](https://github.com/sreyun/openaiops) / [Gitee](https://gitee.com/bigdatasafe/openaiops)

---

## 🚀 Schnellstart

> Der Server **benötigt** PostgreSQL und VictoriaMetrics.

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

Installation → **[../getting-started/install.en.md](../getting-started/install.en.md)** · Produktion → **[../getting-started/deploy.en.md](../getting-started/deploy.en.md)**

---

## 🏗 Architektur

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

## 📸 Produkt-Screenshots

### Web-Konsole

<table>
  <tr>
    <td align="center"><b>Übersichts-Dashboard</b><br/><br/><img src="../../image/1-shouye.png" alt="Übersichts-Dashboard" width="100%"><br/>Einheitliche Ansicht der Cluster-Ressourcen, Alarme und Aktivitäten: Host-Online-Rate, Systemgesundheitsstatus, aktive Alarme Übersicht; CPU / GPU / Speicher / Festplatte / IO / IOPS Ressourcen TOP10 Echtzeit-Ranking, Engpass-Hosts auf einen Blick identifizieren.</td>
    <td align="center"><b>Host-Verwaltung</b><br/><br/><img src="../../image/2-zhuji.png" alt="Host-Verwaltung" width="100%"><br/>Linker Asset-Baum nach Rechenzentrum / Geschäft gruppiert, rechte Kartenansicht zeigt Echtzeit-Metriken jedes Hosts: CPU, Speicher, Swap, Festplattenpartitionen, 1/5/15 Min Last, Netzwerk-Durchsatz, IOPS, Prozess- und Verbindungsanzahl, unterstützt Gitter / Listen-Dualansicht.</td>
  </tr>
  <tr>
    <td align="center"><b>Web-Terminal</b><br/><br/><img src="../../image/3-zhongduan.png" alt="Web-Terminal" width="100%"><br/>Direkte Verbindung zu Zielhosts über Agent-Rückwärtskanal, keine Notwendigkeit, SSH-Eingangsports zu öffnen. Unterstützt Multi-Tab-Verbindungen zu mehreren Hosts, Befehlsprüfung und Aufzeichnungswiedergabe, Beobachtermodus.</td>
    <td align="center"><b>Remote-Desktop</b><br/><br/><img src="../../image/4-zhuomian.png" alt="Remote-Desktop" width="100%"><br/>JPEG / H.264 Dual-Encoding-Remote-Desktop, unterstützt Multi-Screen-Umschaltung, adaptive Auflösung, System-Shortcuts wie Strg+Alt+Entf; rechtes Panel bietet Datei-Upload/Download und Zwischenablagen-Synchronisation, Betriebserlebnis nahe am lokalen Desktop.</td>
  </tr>
  <tr>
    <td align="center"><b>Agent-Installation</b><br/><br/><img src="../../image/5-agent.png" alt="Agent-Installation" width="100%"><br/>Ein Befehl zur Agent-Bereitstellung, unterstützt Linux / Windows / macOS drei Plattformen. Optional Standardmodus, Gateway-Relaismodus, Multi-Server-Push-Modus; Token-Strategie und Auto-Update-Strategie können in der Konsole einheitlich verwaltet werden.</td>
    <td align="center"><b>Hardware-Ressourcen-Überwachung</b><br/><br/><img src="../../image/6-jiqi.png" alt="Hardware-Ressourcen-Überwachung" width="100%"><br/>Out-of-Band-Sammlung des physischen Server-Hardware-Status über Redfish / BMC / iDRAC / iLO: Hersteller, Modell, Seriennummer, Strom/Temperatur/Stromverbrauch, BIOS-Version; BMC-Ereignisprotokolle (SEL) vollständig aufbewahrt, unterstützt KI-Diagnose.</td>
  </tr>
  <tr>
    <td align="center"><b>Container-Verwaltung</b><br/><br/><img src="../../image/7-docker.png" alt="Container-Verwaltung" width="100%"><br/>Einheitliche Verwaltung von Docker / Podman Containern und Compose-Projekten auf Hosts: Echtzeit-Status, Port-Mapping, Image-Informationen auf einen Blick; unterstützt Ein-Klick-Start/Stopp, Neustart, Protokollanzeige, Cross-Host-Stapelfilterung.</td>
    <td align="center"><b>Playbook-Orchestrierung</b><br/><br/><img src="../../image/8-juben.png" alt="Playbook-Orchestrierung" width="100%"><br/>Visuelle Automatisierungs-Playbooks: Systeminspektion, Netzwerkinspektion, Sicherheitsinspektion, systemd-Service-Neustart, K8s Deployment Rolling-Neustart, tiefe Host-Inspektion, Java-Anwendungsinspektion/Leistungsanalyse/Ausnahmeanalyse und andere eingebaute Playbooks sofort einsatzbereit, unterstützt benutzerdefinierte Multi-Step-Parallelität und Genehmigungs-Schutzplanken.</td>
  </tr>
  <tr>
    <td align="center"><b>SRE-Zentrum</b><br/><br/><img src="../../image/9-sre.png" alt="SRE-Zentrum" width="100%"><br/>Alarm-Auslöser / SLO-Burn-Down / manuell erstellte Ereignisse konvergieren hier, mit vollständiger Timeline und automatischen Wiederherstellungsprotokollen. Unterstützt acht Untermodule: Vorfälle, automatische Wiederherstellung, Abhängigkeits-Topologie, SLO, Tickets, On-call, Änderungen, Plattform-Gesundheitsprüfung.</td>
    <td align="center"><b>KI-Diagnose</b><br/><br/><img src="../../image/10-ai.png" alt="KI-Diagnose" width="100%"><br/>Ein-Klick-KI-Assistent in der SRE-Ereignisliste, analysiert automatisch die aktuelle Alarm-Ursache und gibt Entsorgungsvorschläge. KI überprüft Alarmkorrelationen, ruft ähnliche Fälle ab, prüft den Gesundheitszustand kritischer Hosts, Denkprozess vollständig sichtbar.</td>
  </tr>
  <tr>
    <td align="center"><b>Alarm-Einstellungen</b><br/><br/><img src="../../image/11-setting.png" alt="Alarm-Einstellungen" width="100%"><br/>Multi-Kanal-Alarm-Push-Konfiguration: Feishu, DingTalk, Webhook, E-Mail, SMS, Telefon sechs Kanäle wählbar; unterstützt Silence / Inhibit / Routing-Strategien, kritisches geht an Telefon SMS, Warnungen gehen an IM, vermeidet Alarmstürme.</td>
    <td align="center"><b>KI-Einstellungen</b><br/><br/><img src="../../image/12-aiset.png" alt="KI-Einstellungen" width="100%"><br/>One-Stop-KI-Fähigkeitskonfiguration: Dialogmodelle (OpenAI-kompatibel / Bailian / DeepSeek / Ollama / Anthropic / Claude), RAG-Vektorbibliothek, Urteil und Kosten (MoA / Stückpreis), MCP-Integration, Anrufbeobachtung, Sicherheitsautorisierung sechs Einstellungen, unterstützt Spracheingabe/Ausstrahlung.</td>
  </tr>
</table>

### Mobile App (Android / HarmonyOS)

> **Hinweis**: Mobile App (Android / HarmonyOS) ist ein separates Verteilungspaket, **die Open-Source-Community-Version stellt keine App-Installationspakete bereit**. Wenn Sie die mobile Version verwenden müssen, wenden Sie sich bitte an das Projektteam.

<table>
  <tr>
    <td align="center"><b>SRE-Cockpit</b><br/><br/><img src="../../image/app01.jpg" alt="SRE-Cockpit" width="100%"><br/>Mobile Übersichtsseite: Host-Online-Rate, schwere/warnende Alarmzahlen auf einen Blick; schnelle Zugriffe decken Hardware-Überwachung, virtuelle Maschinen, Netzwerkverkehr, Dial-Tests, Host-Überwachung, Protokollsuche, Betriebsorchestrierung, Dashboards ab; ausstehende Vorfälle nach Priorität sortiert.</td>
    <td align="center"><b>Infrastruktur-Überwachung</b><br/><br/><img src="../../image/app02.jpg" alt="Infrastruktur-Überwachung" width="100%"><br/>Mobile Infrastruktur-Seite: vier Dimensionen Host/Ressource/Netzwerk/Dial-Test-Umschaltung; GPU-Ressourcen-Übersicht (Modell, VRAM, Temperatur); Host-Liste nach Gruppe gefiltert, Echtzeit-Anzeige von CPU, Speicher, Festplatte und anderen Kernmetriken.</td>
  </tr>
  <tr>
    <td align="center"><b>Mobiles Terminal</b><br/><br/><img src="../../image/app03.jpg" alt="Mobiles Terminal" width="100%"><br/>Mobiles Web-Terminal: direkte Verbindung zu Zielhosts über Agent-Rückwärtskanal, vollständiges Terminal-Interaktionserlebnis; unterstützt Shortcuts, Schrift-Skalierung, Bildschirmdrehung, Fehlerbehebung jederzeit überall.</td>
    <td align="center"><b>KI-Betriebsassistent</b><br/><br/><img src="../../image/app04.jpg" alt="KI-Betriebsassistent" width="100%"><br/>Mobile KI-Dialog: Probleme in natürlicher Sprache beschreiben, KI ruft automatisch historische Fälle ab, zieht Alarmdetails, prüft Host-Gesundheitszustand, gibt Ursachenanalyse und Entsorgungsvorschläge; untere Navigationsleiste deckt Übersicht/Überwachung/Alarme/Betrieb/KI fünf Haupteinträge ab.</td>
  </tr>
</table>

---

## 📚 Dokumentation

Lange Texte und lokalisierte READMEs liegen unter [`docs/`](../README.md). Im Root bleiben nur das chinesische README und das Changelog.

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

## 🤝 Mitwirken

Issues, PRs und Übersetzungen willkommen. Empfohlen: `make build` · `make audit`.

Wenn AIOps einen Klebe-Stack ersetzt: **bitte einen Star** — das hält das Projekt sichtbar und wartbar.

---

## Lizenz

[AGPL-3.0](../../LICENSE). Kein Host-Cap. Mobile Clients als separate Pakete (Quellcode nicht in diesem Repo).

---

<p align="center">
  <b>AIOps · Ops-Komplexität in eine Plattform, die Sie besitzen.</b><br/>
  <sub>Star ⭐ · Fork · Issue · Self-hosted Ops gemeinsam bauen</sub>
</p>
