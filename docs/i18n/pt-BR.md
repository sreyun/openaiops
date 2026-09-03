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

## 📸 Capturas do produto

### Console Web

<table>
  <tr>
    <td align="center"><b>Painel de visão geral</b><br/><br/><img src="../../image/1-shouye.png" alt="Painel de visão geral" width="100%"><br/>Visão unificada dos recursos do cluster, alertas e atividades: taxa de hosts online, status de saúde do sistema, visão geral de alertas ativos; TOP10 de recursos CPU / GPU / memória / disco / IO / IOPS em tempo real, localize gargalos de hosts de relance.</td>
    <td align="center"><b>Gerenciamento de hosts</b><br/><br/><img src="../../image/2-zhuji.png" alt="Gerenciamento de hosts" width="100%"><br/>Árvore de ativos esquerda agrupada por datacenter / negócio, exibição direita em cartões com métricas em tempo real de cada host: CPU, memória, swap, partições de disco, carga 1/5/15 min, taxa de rede, IOPS, processos e contagem de conexões, suporta visualização dupla grade / lista.</td>
  </tr>
  <tr>
    <td align="center"><b>Terminal Web</b><br/><br/><img src="../../image/3-zhongduan.png" alt="Terminal Web" width="100%"><br/>Conexão direta aos hosts de destino via canal reverso do Agent, sem necessidade de abrir portas SSH de entrada. Suporta multi-abas para múltiplos hosts, auditoria de comandos e reprodução de gravações, modo observador.</td>
    <td align="center"><b>Área de trabalho remota</b><br/><br/><img src="../../image/4-zhuomian.png" alt="Área de trabalho remota" width="100%"><br/>Área de trabalho remota com codificação dupla JPEG / H.264, suporta troca multi-tela, resolução adaptativa, atalhos do sistema como Ctrl+Alt+Del; painel direito fornece upload/download de arquivos e sincronização da área de transferência, experiência operacional próxima à área de trabalho local.</td>
  </tr>
  <tr>
    <td align="center"><b>Instalação do Agent</b><br/><br/><img src="../../image/5-agent.png" alt="Instalação do Agent" width="100%"><br/>Um comando para implantar o Agent, suporta Linux / Windows / macOS três plataformas. Modo padrão, modo relé de gateway, modo push multi-servidor opcionais; estratégia de Token e estratégia de auto-atualização gerenciadas uniformemente no console.</td>
    <td align="center"><b>Monitoramento de hardware</b><br/><br/><img src="../../image/6-jiqi.png" alt="Monitoramento de hardware" width="100%"><br/>Coleta fora de banda do status do hardware do servidor físico via Redfish / BMC / iDRAC / iLO: fabricante, modelo, número de série, alimentação/temperatura/consumo de energia, versão BIOS; logs de eventos BMC (SEL) totalmente conservados, suporta diagnóstico IA.</td>
  </tr>
  <tr>
    <td align="center"><b>Gerenciamento de contêineres</b><br/><br/><img src="../../image/7-docker.png" alt="Gerenciamento de contêineres" width="100%"><br/>Gerenciamento unificado de contêineres Docker / Podman e projetos Compose nos hosts: status em tempo real, mapeamento de portas, informações de imagem de relance; suporta início/parada com um clique, reinício, visualização de logs, filtragem em lote entre hosts.</td>
    <td align="center"><b>Orquestração de Playbooks</b><br/><br/><img src="../../image/8-juben.png" alt="Orquestração de Playbooks" width="100%"><br/>Playbooks de operações automatizadas visuais: inspeção do sistema, inspeção de rede, inspeção de segurança, reinício de serviços systemd, reinício progressivo de K8s Deployment, inspeção profunda de hosts, inspeção de aplicações Java/análise de desempenho/análise de exceções e outros playbooks integrados prontos para uso, suporta paralelismo multi-etapa personalizado e guardas de aprovação.</td>
  </tr>
  <tr>
    <td align="center"><b>Centro SRE</b><br/><br/><img src="../../image/9-sre.png" alt="Centro SRE" width="100%"><br/>Os gatilhos de alertas / burn-down SLO / eventos criados manualmente convergem aqui, com linha do tempo completa e registros de auto-remediação. Suporta oito submódulos: incidentes, auto-remediação, topologia de dependências, SLO, tickets, On-call, mudanças, inspeção de saúde da plataforma.</td>
    <td align="center"><b>Diagnóstico IA</b><br/><br/><img src="../../image/10-ai.png" alt="Diagnóstico IA" width="100%"><br/>Assistente IA com um clique na lista de eventos SRE, analisa automaticamente a causa raiz do alerta atual e dá sugestões de disposição. IA revisa correlações de alertas, recupera casos semelhantes, verifica o status de saúde de hosts críticos, processo de pensamento totalmente visível.</td>
  </tr>
  <tr>
    <td align="center"><b>Configurações de alerta</b><br/><br/><img src="../../image/11-setting.png" alt="Configurações de alerta" width="100%"><br/>Configuração de push de alertas multi-canal: Feishu, DingTalk, Webhook, e-mail, SMS, telefone seis canais opcionais; suporta estratégias de silêncio / inibição / roteamento, o crítico vai para telefone SMS, avisos vão para IM, evita tempestades de alertas.</td>
    <td align="center"><b>Configurações IA</b><br/><br/><img src="../../image/12-aiset.png" alt="Configurações IA" width="100%"><br/>Configuração de capacidades IA tudo em um: modelos de diálogo (compatível OpenAI / Bailian / DeepSeek / Ollama / Anthropic / Claude), biblioteca vetorial RAG, julgamento e custo (MoA / preço unitário), integração MCP, observação de chamadas, autorização de segurança seis configurações, suporta entrada de voz/transmissão.</td>
  </tr>
</table>

### App Móvel (Android / HarmonyOS)

> **Nota**: O App Móvel (Android / HarmonyOS) é um pacote de distribuição separado, **a versão comunitária de código aberto não fornece pacotes de instalação do App**. Se você precisa usar o móvel, entre em contato com a equipe do projeto.

<table>
  <tr>
    <td align="center"><b>Cabine SRE</b><br/><br/><img src="../../image/app01.jpg" alt="Cabine SRE" width="100%"><br/>Página de visão geral móvel: taxa de hosts online, contagens de alertas graves/avisos de relance; entradas rápidas cobrem monitoramento de hardware, máquinas virtuais, tráfego de rede, testes de discagem, monitoramento de hosts, pesquisa de logs, orquestração de operações, painéis; incidentes pendentes ordenados por prioridade.</td>
    <td align="center"><b>Monitoramento de infraestrutura</b><br/><br/><img src="../../image/app02.jpg" alt="Monitoramento de infraestrutura" width="100%"><br/>Página de infraestrutura móvel: quatro dimensões host/recurso/rede/teste de discagem comutáveis; visão geral de recursos GPU (modelo, VRAM, temperatura); lista de hosts filtrada por grupo, exibição em tempo real de métricas principais de CPU, memória, disco.</td>
  </tr>
  <tr>
    <td align="center"><b>Terminal Móvel</b><br/><br/><img src="../../image/app03.jpg" alt="Terminal Móvel" width="100%"><br/>Terminal web móvel: conexão direta aos hosts de destino via canal reverso do Agent, experiência interativa completa de terminal; suporta teclas de atalho, escalonamento de fonte, rotação de tela, solução de problemas a qualquer hora em qualquer lugar.</td>
    <td align="center"><b>Assistente de Ops IA</b><br/><br/><img src="../../image/app04.jpg" alt="Assistente de Ops IA" width="100%"><br/>Diálogo IA móvel: descreva problemas em linguagem natural, IA recupera automaticamente casos históricos, puxa detalhes de alertas, verifica o status de saúde do host, dá análise de causa raiz e sugestões de disposição; a barra de navegação inferior cobre as cinco entradas principais visão geral/monitoramento/alertas/operações/IA.</td>
  </tr>
</table>

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
