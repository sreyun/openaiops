<div align="center">

# AIOps

**Открытая self-hosted платформа мониторинга хостов и SRE**  
Наблюдение · Алерты · Автовосстановление · Удалённые ops · Agent OTA · ИИ-диагностика — один бинарник под вашим контролем.

[![Version](https://img.shields.io/badge/Version-v0.20.49-blue)](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](../../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Android%20%7C%20HarmonyOS-lightgrey)]()
[![Stars](https://img.shields.io/github/stars/sreyun/openaiops?style=social)](https://github.com/sreyun/openaiops)

**[简体中文](../../README.md) · [繁體中文](zh-TW.md) · [English](en.md) · [日本語](ja.md) · [한국어](ko.md) · [Français](fr.md) · [Deutsch](de.md) · [Español](es.md) · [Português](pt-BR.md) · [Русский](ru.md)**

[Быстрый старт](#-быстрый-старт) · [Ключевые возможности](#-ключевые-возможности) · [Документация](../README.md) · [Changelog](../../CHANGELOG.md) · [Releases](https://github.com/sreyun/openaiops/releases)

</div>

---

## Зачем AIOps

Стеки ops разрастаются: метрики, алерты, bastion и runbook по отдельности. Коммерческие продукты тарифицируют по хостам, а данные остаются в их облаке.

AIOps сводит обычный путь в **одну self-hosted платформу**:

| | AIOps | Типичный «склеенный» стек |
|---|---|---|
| **Состав** | 1 Go-сервер + 1 агент без зависимостей | Zabbix / Prometheus / Grafana / Alertmanager / bastion / runbooks… |
| **Время до результата** | `docker compose up -d` (~3 мин) | Дни интеграции |
| **Данные** | PostgreSQL + VictoriaMetrics, **ваши** | SaaS или разрозненные БД |
| **Удалённо** | Web-терминал / рабочий стол / port-forward; агент **только исходящий** | Отдельный VPN / bastion |
| **Флот** | **Авто OTA Agent** (SHA-256, окно обслуживания, пакетный push, rollback) | SSH-замена на каждом хосте |
| **Контур** | Алерт → playbook → инцидент/SLO/тикет → ИИ RCA | Люди закрывают разрывы |
| **Лицензия** | **AGPL-3.0**, без лимита хостов | За узел / модуль |

> Для частных ЦОД, гибридного облака и команд, которым нужны видимость, контроль, безопасность изменений и объяснимые ops.

---

## ✨ Ключевые возможности

Семь столбов — не бесконечный список функций :

```
  Observe ──────► Govern ──────► Remediate ──────► Diagnose
  Hosts/GPU/logs   Silence/route   Playbooks/gates   AI · RAG · MCP
  Probes/OOB       Multi-channel   Incident/SLO      Evidence gate

  Remote · terminal/desktop/forward (reverse tunnel)   Fleet · Agent OTA
  Security · RBAC/MFA/FIM
```

1. **Наблюдение** — кроссплатформенный агент (Linux / Windows / macOS / Kylin), GPU, логи, HTTP/TCP-пробы, API SLI, Redfish / SNMP / NetFlow / контейнеры / K8s / Hyper-V.
2. **Управление** — пороги, silence / inhibit / route; Feishu / DingTalk / e-mail / SMS / голос.
3. **Восстановление и SRE** — playbook с approval-гарантиями; инциденты, SLO, тикеты, окна заморозки, аудируемый break-glass.
4. **ИИ-диагностика** — инспекция + RCA (модели совместимые с OpenAI; иначе эвристика); RAG на pgvector, Skills, MCP (Cursor / Claude); самотест речи.
5. **Удалённые ops** — web-терминал (replay, наблюдение, аудит, второй пароль), удалённый рабочий стол (JPEG/H.264), port-forward / HTTP-прокси с защитой SSRF.
6. **Безопасная поставка** — RBAC, MFA, fingerprint агента, AES-256-GCM; веб-консоль; Android / HarmonyOS отдельно.
7. **Agent OTA** — после обновления сервера отстающие online-агенты автоматически ставятся в очередь (по умолчанию ВКЛ); пакетный push из консоли или `POST /api/v1/agents/update`; загрузка `/dl/` с SHA-256, rollback `.bak`.

Текущий релиз **[v0.20.49](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)** · Зеркала: [GitHub](https://github.com/sreyun/openaiops) / [Gitee](https://gitee.com/bigdatasafe/openaiops)

---

## 🚀 Быстрый старт

> Серверу **нужны** и PostgreSQL, и VictoriaMetrics.

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

Установка → **[../getting-started/install.en.md](../getting-started/install.en.md)** · Прод → **[../getting-started/deploy.en.md](../getting-started/deploy.en.md)**

---

## 🏗 Архитектура

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

## 📚 Документация

Длинные тексты и локализованные README — в [`docs/`](../README.md). В корне остаются только китайский README и changelog.

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

## 🤝 Участие

Issues, PR и переводы приветствуются. Рекомендуем: `make build` · `make audit`.

Если AIOps заменил «склеенный» стек — **поставьте Star**: это поддерживает видимость и развитие проекта.

---

## Лицензия

[AGPL-3.0](../../LICENSE). Без лимита хостов. Мобильные клиенты — отдельные пакеты (исходников нет в этом репозитории).

---

<p align="center">
  <b>AIOps · Сверните сложность ops в платформу, которой владеете вы.</b><br/>
  <sub>Star ⭐ · Fork · Issue · Строим self-hosted ops вместе</sub>
</p>
