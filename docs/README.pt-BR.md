<div align="center">

# AIOps

**Console de operações self-hosted sem portas de entrada: ver hosts · abrir terminal/desktop · controlar alertas**

[![Version](https://img.shields.io/badge/Version-v1.0.6-blue)](https://github.com/sreyun/aiops/releases/tag/v1.0.6)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS-lightgrey)]()

**[简体中文](README.md) · [繁體中文](README.zh-TW.md) · [English](README_EN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt-BR.md) · [Русский](README.ru.md)**

</div>

> Muitas máquinas ficam atrás de NAT/firewall: o agent instala, a porta de entrada quase nunca.  
> O AIOps usa um **agent com conexão de saída** para unir monitoramento, terminal/desktop web e alertas em um plano de controle self-hosted.

**Versão [v1.0.6](https://github.com/sreyun/aiops/releases/tag/v1.0.6)** · [GitHub](https://github.com/sreyun/aiops) / [Gitee](https://gitee.com/bigdatasafe/aiops) · [CHANGELOG](CHANGELOG.md)

> Docs de instalação detalhados: **[English](INSTALL_EN.md)** / [中文](INSTALL.md).

---

## Comece por aqui (~3 minutos)

```bash
docker compose up -d
open http://localhost:8529
# Copie o comando de instalação da UI e execute no host alvo
# curl -fsSL "http://<server>:8529/install.sh?token=<TOKEN>" | sudo sh
```

Depois valide:

1. **Host online** com métricas de CPU/RAM/disco  
2. **Terminal web** abre um shell  
3. **Um alerta de limiar** é entregue  

Esse é o caminho principal: **console ops com reverse-connect**. O restante se apoia nele.

---

## Por que AIOps

| | |
|---|---|
| **Reverse connect** | O agent sai; terminal, desktop e forwards compartilham o túnel |
| **Binário único + agent sem deps** | Um servidor Go; agent stdlib em Linux/Windows/macOS/Kylin |
| **Seus dados com você** | PostgreSQL + VictoriaMetrics, totalmente auto-hospedado, sem telemetria forçada |

> O produto pode ser amplo — **a porta de entrada permanece estreita.**

---

## Mapa de capacidades

```
Caminho principal
  Métricas de host → governança de alertas → terminal/desktop web → port forward

Extensões
  Probes / API · logs · playbooks · SRE · AI / MCP · segurança
  Hyper-V / containers / K8s · SNMP / NetFlow / Redfish · SQL · mobile*
```

<details>
<summary><b>Hosts e recursos</b></summary>

- Métricas nativas (GPU); out-of-band (Redfish, NetFlow, SNMP, inventários)  
- Busca global e ajudas de topologia  

</details>

<details>
<summary><b>Alertas e observabilidade</b></summary>

- Limiares + silence/inhibit/route; Feishu/DingTalk/e-mail/SMS/voz  
- Probes Ping/TCP/HTTP/processo; disponibilidade de API / P95  
- Logs + busca; séries no VictoriaMetrics  

</details>

<details>
<summary><b>Terminal, desktop e forward</b></summary>

- Terminal web (replay, auditoria, segunda senha)  
- Desktop web JPEG/H.264; tela de bloqueio Windows exige **instalação como serviço**  
- Port forward / proxy HTTP (WebSocket), proteção SSRF  

</details>

<details>
<summary><b>Automação, SRE e IA</b></summary>

- Playbooks, remediação com gates, incidentes/SLO/tickets  
- Inspeção AI / RCA; RAG pgvector; Hermes; MCP para Cursor/Claude  

</details>

---

## Instalação

```bash
docker compose up -d
```

Abra `http://localhost:8529`, conclua a init de segurança e ative MFA.  
Detalhes: [INSTALL_EN.md](INSTALL_EN.md).

---

## Caminho recomendado

1. Enrolar hosts → confirmar online  
2. Ver métricas → probes opcionais  
3. Governar alertas → IM/e-mail  
4. Troubleshooting remoto → terminal; desktop Windows como serviço  
5. Expandir → playbooks, SRE, AI/MCP, segurança  

UI clássica: `/` · Vue: `/v2/`

---

## Arquitetura

```
Navegador/mobile* ──REST/WS──► Servidor Go ──► PostgreSQL + VictoriaMetrics
                                   ▲
                          Conexão de saída
                                   │
                              Agent Go
```

Os dois stores são obrigatórios. Licença **AGPL-3.0**: [LICENSE](../LICENSE) — gratuita e sem limite de hosts para uso interno auto-hospedado; **licença comercial** é necessária para distribuição proprietária, integração ou oferta como serviço de rede: [LICENSING.md](../LICENSING.md).  
Contribuições bem-vindas no caminho principal. Repo: <https://github.com/sreyun/aiops>

---

<p align="center"><b>Primeiro: instalar o agent → ver o host → abrir o terminal.</b></p>
