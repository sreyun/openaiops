<div align="center">

# AIOps

**Consola de operaciones autoalojada sin puertos de entrada: ver hosts · abrir terminal/escritorio · controlar alertas**

[![Version](https://img.shields.io/badge/Version-v1.0.6-blue)](https://github.com/sreyun/aiops/releases/tag/v1.0.6)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS-lightgrey)]()

**[简体中文](README.md) · [繁體中文](README.zh-TW.md) · [English](README_EN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt-BR.md) · [Русский](README.ru.md)**

</div>

> Muchas máquinas están detrás de NAT/firewall: puedes instalar el agent, no abrir puertos de entrada.  
> AIOps usa un **agent con conexión saliente** para unir monitoreo, terminal/escritorio web y alertas en un plano de control autoalojado.

**Versión [v1.0.6](https://github.com/sreyun/aiops/releases/tag/v1.0.6)** · [GitHub](https://github.com/sreyun/aiops) / [Gitee](https://gitee.com/bigdatasafe/aiops) · [CHANGELOG](CHANGELOG.md)

> Documentación de instalación detallada: **[English](INSTALL_EN.md)** / [中文](INSTALL.md).

---

## Empieza aquí (~3 minutos)

```bash
docker compose up -d
open http://localhost:8529
# Copia el comando de instalación de la UI y ejecútalo en el host
# curl -fsSL "http://<server>:8529/install.sh?token=<TOKEN>" | sudo sh
```

Luego verifica:

1. **Host en línea** con métricas CPU/RAM/disco  
2. **Terminal web** abre un shell  
3. **Una alerta de umbral** llega por IM/correo  

Ese es el camino punta de lanza: **consola ops con reverse-connect**. Lo demás se apoya en él.

---

## Por qué AIOps

| | |
|---|---|
| **Reverse connect** | El agent sale; terminal, escritorio y forwards comparten el túnel |
| **Binario único + agent sin deps** | Un servidor Go; agent stdlib en Linux/Windows/macOS/Kylin |
| **Tus datos contigo** | PostgreSQL + VictoriaMetrics, MIT, sin gates |

> El producto puede ser amplio — **la puerta de entrada se mantiene estrecha.**

---

## Mapa de capacidades

```
Camino principal
  Métricas de host → gobernanza de alertas → terminal/escritorio web → port forward

Extensiones
  Probes / API · logs · playbooks · SRE · AI / MCP · seguridad
  Hyper-V / contenedores / K8s · SNMP / NetFlow / Redfish · SQL · móvil*
```

<details>
<summary><b>Hosts y recursos</b></summary>

- Métricas nativas (GPU); fuera de banda (Redfish, NetFlow, SNMP, inventarios)  
- Búsqueda global y ayudas de topología  

</details>

<details>
<summary><b>Alertas y observabilidad</b></summary>

- Umbrales + silence/inhibit/route; Feishu/DingTalk/correo/SMS/voz  
- Probes Ping/TCP/HTTP/proceso; disponibilidad API / P95  
- Logs + búsqueda; series en VictoriaMetrics  

</details>

<details>
<summary><b>Terminal, escritorio y forward</b></summary>

- Terminal web (replay, auditoría, segunda contraseña)  
- Escritorio web JPEG/H.264; bloqueo Windows requiere **instalación como servicio**  
- Port forward / proxy HTTP (WebSocket), protección SSRF  

</details>

<details>
<summary><b>Automatización, SRE e IA</b></summary>

- Playbooks, remediación con puertas, incidentes/SLO/tickets  
- Inspección AI / RCA; RAG pgvector; Hermes; MCP para Cursor/Claude  

</details>

---

## Instalación

```bash
docker compose up -d
```

Abre `http://localhost:8529`, completa la init de seguridad y activa MFA.  
Detalle: [INSTALL_EN.md](INSTALL_EN.md).

---

## Camino recomendado

1. Enrolar hosts → confirmar online  
2. Ver métricas → probes opcionales  
3. Gobernar alertas → IM/correo  
4. Troubleshooting remoto → terminal; escritorio Windows como servicio  
5. Ampliar → playbooks, SRE, AI/MCP, seguridad  

UI clásica: `/` · Vue: `/v2/`

---

## Arquitectura

```
Navegador/móvil* ──REST/WS──► Servidor Go ──► PostgreSQL + VictoriaMetrics
                                  ▲
                         Conexión saliente
                                  │
                             Agent Go
```

Ambos stores son obligatorios. Licencia **MIT**: [LICENSE](LICENSE).  
Contribuciones bienvenidas en el camino principal. Repo: <https://github.com/sreyun/aiops>

---

<p align="center"><b>Primero: instalar agent → ver host → abrir terminal.</b></p>
