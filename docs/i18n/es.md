<div align="center">

# AIOps

**Plataforma open-source autoalojada de monitorización de hosts y SRE**  
Observar · Alertar · Remediar · Ops remotas · Agent OTA · Diagnóstico IA — un binario bajo tu control.

[![Version](https://img.shields.io/badge/Version-v0.20.49-blue)](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](../../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Android%20%7C%20HarmonyOS-lightgrey)]()
[![Stars](https://img.shields.io/github/stars/sreyun/openaiops?style=social)](https://github.com/sreyun/openaiops)

**[简体中文](../../README.md) · [繁體中文](zh-TW.md) · [English](en.md) · [日本語](ja.md) · [한국어](ko.md) · [Français](fr.md) · [Deutsch](de.md) · [Español](es.md) · [Português](pt-BR.md) · [Русский](ru.md)**

[Inicio rápido](#-inicio-rápido) · [Capacidades clave](#-capacidades-clave) · [Documentación](../README.md) · [Registro de cambios](../../CHANGELOG.md) · [Releases](https://github.com/sreyun/openaiops/releases)

</div>

---

## Por qué AIOps

Las pilas de ops crecen: métricas, alertas, bastión y runbooks por separado. Los productos comerciales cobran por host y dejan tus datos en su nube.

AIOps concentra el camino habitual en **una plataforma autoalojada**:

| | AIOps | Stack típico “pegado” |
|---|---|---|
| **Piezas** | 1 servidor Go + 1 agente sin dependencias | Zabbix / Prometheus / Grafana / Alertmanager / bastión / runbooks… |
| **Puesta en marcha** | `docker compose up -d` (~3 min) | Días de integración |
| **Datos** | PostgreSQL + VictoriaMetrics, **tuyos** | SaaS o BD dispersas |
| **Remoto** | Terminal / escritorio / port-forward web; agente **solo saliente** | VPN / bastión extra |
| **Flota** | **OTA automático de Agent** (SHA-256, ventana de mantenimiento, push por lotes, rollback) | Sustitución SSH por host |
| **Bucle** | Alerta → playbook → incidente/SLO/ticket → RCA IA | Personas unen huecos |
| **Licencia** | **AGPL-3.0**, sin tope de hosts | Por nodo / módulo |

> Para DC privados, nube híbrida y equipos que necesitan visibilidad, control, seguridad del cambio y ops explicables.

---

## ✨ Capacidades clave

Siete pilares — no una lista interminable :

```
  Observe ──────► Govern ──────► Remediate ──────► Diagnose
  Hosts/GPU/logs   Silence/route   Playbooks/gates   AI · RAG · MCP
  Probes/OOB       Multi-channel   Incident/SLO      Evidence gate

  Remote · terminal/desktop/forward (reverse tunnel)   Fleet · Agent OTA
  Security · RBAC/MFA/FIM
```

1. **Observar** — Agente multiplataforma (Linux / Windows / macOS / Kylin), GPU, logs, sondas HTTP/TCP, SLI de API, Redfish / SNMP / NetFlow / contenedores / K8s / Hyper-V.
2. **Gobernar** — Umbrales, silence / inhibit / route; Feishu / DingTalk / correo / SMS / voz.
3. **Remediar y SRE** — Playbooks con aprobaciones; incidentes, SLO, tickets, ventanas de congelación, break-glass auditado.
4. **Diagnóstico IA** — Inspección + RCA (modelos compatibles OpenAI; heurística si no hay modelo); RAG pgvector, Skills, MCP (Cursor / Claude); autotest de voz.
5. **Ops remotas** — Terminal web (replay, observar, auditoría, contraseña secundaria), escritorio remoto (JPEG/H.264), port-forward / proxy HTTP con protección SSRF.
6. **Entrega segura** — RBAC, MFA, huella del agente, cifrado AES-256-GCM; consola Web; Android / HarmonyOS por separado.
7. **Agent OTA** — Tras actualizar el servidor, los agents en línea rezagados se encolan solos (ON por defecto); push por lotes en consola o `POST /api/v1/agents/update`; descarga `/dl/` con SHA-256, rollback `.bak`.

Versión actual **[v0.20.49](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)** · Espejos: [GitHub](https://github.com/sreyun/openaiops) / [Gitee](https://gitee.com/bigdatasafe/openaiops)

---

## 🚀 Inicio rápido

> El servidor **requiere** PostgreSQL y VictoriaMetrics.

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

Instalación → **[../getting-started/install.en.md](../getting-started/install.en.md)** · Producción → **[../getting-started/deploy.en.md](../getting-started/deploy.en.md)**

---

## 🏗 Arquitectura

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

## 📸 Capturas de producto

### Consola Web

<table>
  <tr>
    <td align="center"><b>Panel de resumen</b><br/><br/><img src="../image/1-shouye.png" alt="Panel de resumen" width="100%"><br/>Vista unificada de recursos del clúster, alertas y actividades: tasa de hosts en línea, estado de salud del sistema, resumen de alertas activas; TOP10 de recursos CPU / GPU / memoria / disco / IO / IOPS en tiempo real, localiza cuellos de botella de un vistazo.</td>
    <td align="center"><b>Gestión de hosts</b><br/><br/><img src="../image/2-zhuji.png" alt="Gestión de hosts" width="100%"><br/>Árbol de activos izquierdo agrupado por datacenter / negocio, vista derecha en tarjetas con métricas en tiempo real de cada host: CPU, memoria, swap, particiones de disco, carga 1/5/15 min, rendimiento de red, IOPS, procesos y conexiones, vista dual cuadrícula / lista.</td>
  </tr>
  <tr>
    <td align="center"><b>Terminal Web</b><br/><br/><img src="../image/3-zhongduan.png" alt="Terminal Web" width="100%"><br/>Conexión directa a hosts objetivo a través del canal inverso del Agent, sin necesidad de abrir puertos SSH entrantes. Soporta multi-pestaña para múltiples hosts, auditoría de comandos y reproducción de grabaciones, modo observador.</td>
    <td align="center"><b>Escritorio remoto</b><br/><br/><img src="../image/4-zhuomian.png" alt="Escritorio remoto" width="100%"><br/>Escritorio remoto de doble codificación JPEG / H.264, soporta cambio multi-pantalla, resolución adaptativa, atajos del sistema como Ctrl+Alt+Del; panel derecho proporciona carga/descarga de archivos y sincronización del portapapeles, experiencia operativa cercana al escritorio local.</td>
  </tr>
  <tr>
    <td align="center"><b>Instalación del Agent</b><br/><br/><img src="../image/5-agent.png" alt="Instalación del Agent" width="100%"><br/>Un comando para desplegar el Agent, soporta Linux / Windows / macOS tres plataformas. Modo estándar, modo relé de pasarela, modo push multi-servidor opcionales; estrategia de Token y estrategia de auto-actualización gestionadas uniformemente en la consola.</td>
    <td align="center"><b>Monitoreo de hardware</b><br/><br/><img src="../image/6-jiqi.png" alt="Monitoreo de hardware" width="100%"><br/>Recolección fuera de banda del estado del hardware del servidor físico a través de Redfish / BMC / iDRAC / iLO: fabricante, modelo, número de serie, alimentación/temperatura/consumo de energía, versión BIOS; registros de eventos BMC (SEL) conservados completamente, soporta diagnóstico IA.</td>
  </tr>
  <tr>
    <td align="center"><b>Gestión de contenedores</b><br/><br/><img src="../image/7-docker.png" alt="Gestión de contenedores" width="100%"><br/>Gestión unificada de contenedores Docker / Podman y proyectos Compose en hosts: estado en tiempo real, mapeo de puertos, información de imagen de un vistazo; soporta inicio/parada con un clic, reinicio, visualización de logs, filtrado por lotes entre hosts.</td>
    <td align="center"><b>Orquestación de Playbooks</b><br/><br/><img src="../image/8-juben.png" alt="Orquestación de Playbooks" width="100%"><br/>Playbooks de operaciones automatizadas visuales: inspección del sistema, inspección de red, inspección de seguridad, reinicio de servicios systemd, reinicio progresivo de K8s Deployment, inspección profunda de hosts, inspección de aplicaciones Java/análisis de rendimiento/análisis de excepciones y otros playbooks integrados listos para usar, soporta paralelismo multi-paso personalizado y barandillas de aprobación.</td>
  </tr>
  <tr>
    <td align="center"><b>Centro SRE</b><br/><br/><img src="../image/9-sre.png" alt="Centro SRE" width="100%"><br/>Los activadores de alertas / burn-down SLO / eventos creados manualmente convergen aquí, con línea de tiempo completa y registros de auto-remediación. Soporta ocho submódulos: incidentes, auto-remediación, topología de dependencias, SLO, tickets, On-call, cambios, inspección de salud de la plataforma.</td>
    <td align="center"><b>Diagnóstico IA</b><br/><br/><img src="../image/10-ai.png" alt="Diagnóstico IA" width="100%"><br/>Asistente IA con un clic en la lista de eventos SRE, analiza automáticamente la causa raíz de la alerta actual y da sugerencias de disposición. IA revisa correlaciones de alertas, recupera casos similares, verifica el estado de salud de hosts críticos, proceso de pensamiento completamente visible.</td>
  </tr>
  <tr>
    <td align="center"><b>Configuración de alertas</b><br/><br/><img src="../image/11-setting.png" alt="Configuración de alertas" width="100%"><br/>Configuración de push de alertas multi-canal: Feishu, DingTalk, Webhook, correo electrónico, SMS, teléfono seis canales opcionales; soporta estrategias de silencio / inhibición / enrutamiento, lo crítico va al teléfono SMS, las advertencias van al IM, evita tormentas de alertas.</td>
    <td align="center"><b>Configuración IA</b><br/><br/><img src="../image/12-aiset.png" alt="Configuración IA" width="100%"><br/>Configuración de capacidades IA todo en uno: modelos de diálogo (compatible OpenAI / Bailian / DeepSeek / Ollama / Anthropic / Claude), biblioteca vectorial RAG, juicio y costo (MoA / precio unitario), integración MCP, observación de llamadas, autorización de seguridad seis configuraciones, soporta entrada de voz/difusión.</td>
  </tr>
</table>

### App Móvil (Android / HarmonyOS)

> **Nota**: La App Móvil (Android / HarmonyOS) es un paquete de distribución independiente, **la versión comunitaria de código abierto no proporciona paquetes de instalación de la App**. Si necesita usar el móvil, póngase en contacto con el equipo del proyecto.

<table>
  <tr>
    <td align="center"><b>Cabina SRE</b><br/><br/><img src="../image/app01.jpg" alt="Cabina SRE" width="100%"><br/>Página de resumen móvil: tasa de hosts en línea, conteos de alertas graves/advertencia de un vistazo; accesos rápidos cubren monitoreo de hardware, máquinas virtuales, tráfico de red, pruebas de marcado, monitoreo de hosts, búsqueda de logs, orquestación de operaciones, paneles; incidentes pendientes ordenados por prioridad.</td>
    <td align="center"><b>Monitoreo de infraestructura</b><br/><br/><img src="../image/app02.jpg" alt="Monitoreo de infraestructura" width="100%"><br/>Página de infraestructura móvil: cuatro dimensiones host/recurso/red/prueba de marcado conmutables; resumen de recursos GPU (modelo, VRAM, temperatura); lista de hosts filtrada por grupo, visualización en tiempo real de métricas clave de CPU, memoria, disco.</td>
  </tr>
  <tr>
    <td align="center"><b>Terminal Móvil</b><br/><br/><img src="../image/app03.jpg" alt="Terminal Móvil" width="100%"><br/>Terminal web móvil: conexión directa a hosts objetivo a través del canal inverso del Agent, experiencia interactiva completa de terminal; soporta teclas de acceso rápido, escalado de fuente, rotación de pantalla, solución de problemas en cualquier momento y lugar.</td>
    <td align="center"><b>Asistente de Ops IA</b><br/><br/><img src="../image/app04.jpg" alt="Asistente de Ops IA" width="100%"><br/>Diálogo IA móvil: describa problemas en lenguaje natural, IA recupera automáticamente casos históricos, extrae detalles de alertas, verifica el estado de salud del host, da análisis de causa raíz y sugerencias de disposición; la barra de navegación inferior cubre las cinco entradas principales resumen/monitoreo/alertas/operaciones/IA.</td>
  </tr>
</table>

---

## 📚 Documentación

La documentación larga y los README localizados están en [`docs/`](../README.md). En la raíz solo quedan el README chino y el changelog.

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

Issues, PRs y traducciones bienvenidas. Sugerido: `make build` · `make audit`.

Si AIOps reemplaza un stack pegado, **deja una Star** — mantiene el proyecto visible y mantenible.

---

## Licencia

[AGPL-3.0](../../LICENSE). Sin tope de hosts. Clientes móviles en paquetes separados (código fuente fuera de este repo).

---

<p align="center">
  <b>AIOps · Reduce la complejidad ops a una plataforma que posees.</b><br/>
  <sub>Star ⭐ · Fork · Issue · Construyamos ops autoalojadas juntos</sub>
</p>
