<div align="center">

# AIOps

**Self-hosted ops-консоль без входящих портов: видеть хосты · открывать терминал/рабочий стол · управлять алертами**

[![Version](https://img.shields.io/badge/Version-v1.0.6-blue)](https://github.com/sreyun/aiops/releases/tag/v1.0.6)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS-lightgrey)]()

**[简体中文](README.md) · [繁體中文](README.zh-TW.md) · [English](README_EN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt-BR.md) · [Русский](README.ru.md)**

</div>

> Многие машины за NAT/фаерволом: агент поставить можно, входящие порты — редко.  
> AIOps использует **агент с исходящим подключением**, объединяя мониторинг, web-терминал/рабочий стол и алерты в одной self-hosted control plane.

**Релиз [v1.0.6](https://github.com/sreyun/aiops/releases/tag/v1.0.6)** · [GitHub](https://github.com/sreyun/aiops) / [Gitee](https://gitee.com/bigdatasafe/aiops) · [CHANGELOG](CHANGELOG.md)

> Подробные гайды по установке: **[English](INSTALL_EN.md)** / [中文](INSTALL.md).

---

## Начните отсюда (~3 минуты)

```bash
docker compose up -d
open http://localhost:8529
# Скопируйте команду установки из UI и выполните на целевом хосте
# curl -fsSL "http://<server>:8529/install.sh?token=<TOKEN>" | sudo sh
```

Затем проверьте:

1. **Хост online** с метриками CPU/RAM/диска  
2. **Web-терминал** открывает shell  
3. **Один threshold-алерт** доходит до IM/почты  

Это главный путь: **ops-консоль с reverse-connect**. Остальное строится поверх него.

---

## Почему AIOps

| | |
|---|---|
| **Reverse connect** | Агент выходит наружу; терминал, desktop и forwards — в одном туннеле |
| **Один бинарник + agent без зависимостей** | Один Go-сервер; stdlib-агент на Linux/Windows/macOS/Kylin |
| **Данные у вас** | PostgreSQL + VictoriaMetrics, полностью self-hosted, без принудительной телеметрии |

> Платформа может быть широкой — **входная дверь остаётся узкой.**

---

## Карта возможностей

```
Основной путь
  Метрики хостов → governance алертов → web-терминал/desktop → port forward

Расширения
  Probes / API · логи · playbooks · SRE · AI / MCP · security
  Hyper-V / контейнеры / K8s · SNMP / NetFlow / Redfish · SQL · mobile*
```

<details>
<summary><b>Хосты и ресурсы</b></summary>

- Нативные метрики (включая GPU); out-of-band (Redfish, NetFlow, SNMP, inventory)  
- Глобальный поиск и топологические подсказки  

</details>

<details>
<summary><b>Алерты и observability</b></summary>

- Пороги + silence/inhibit/route; Feishu/DingTalk/почта/SMS/голос  
- Probes Ping/TCP/HTTP/процесс; доступность API / P95  
- Логи + поиск; ряды во VictoriaMetrics  

</details>

<details>
<summary><b>Терминал, desktop и forward</b></summary>

- Web-терминал (replay, audit, второй пароль)  
- Web-desktop JPEG/H.264; экран блокировки Windows требует **установки как службы**  
- Port forward / HTTP proxy (WebSocket), защита от SSRF  

</details>

<details>
<summary><b>Автоматизация, SRE и AI</b></summary>

- Playbooks, gated auto-remediation, incidents/SLO/tickets  
- AI inspection / RCA; RAG на pgvector; Hermes; MCP для Cursor/Claude  

</details>

---

## Установка

```bash
docker compose up -d
```

Откройте `http://localhost:8529`, пройдите security init, включите MFA.  
Подробности: [INSTALL_EN.md](INSTALL_EN.md).

---

## Рекомендуемый путь

1. Зарегистрировать хосты → проверить online  
2. Смотреть метрики → при необходимости probes  
3. Настроить алерты → IM/почта  
4. Удалённый troubleshooting → терминал; Windows desktop как служба  
5. Расширять → playbooks, SRE, AI/MCP, security  

Classic UI: `/` · Vue: `/v2/`

---

## Архитектура

```
Браузер/mobile* ──REST/WS──► Go-сервер ──► PostgreSQL + VictoriaMetrics
                                  ▲
                         Исходящее подключение
                                  │
                              Go-агент
```

Оба хранилища обязательны. Лицензия **AGPL-3.0**: [LICENSE](../LICENSE) — бесплатно и без ограничения по числу хостов для внутреннего self-hosted использования; для закрытого распространения, встраивания или предоставления как сетевой сервис нужна **коммерческая лицензия**: [LICENSING.md](../LICENSING.md).  
Контрибуции приветствуются на основном пути. Репозиторий: <https://github.com/sreyun/aiops>

---

<p align="center"><b>Сначала: установить агент → увидеть хост → открыть терминал.</b></p>
