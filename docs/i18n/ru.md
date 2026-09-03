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

## 📸 Скриншоты продукта

### Веб-консоль

<table>
  <tr>
    <td align="center"><b>Обзорная панель</b><br/><br/><img src="../image/1-shouye.png" alt="Обзорная панель" width="100%"><br/>Единый вид ресурсов кластера, оповещений и активностей: процент хостов онлайн, статус здоровья системы, обзор активных оповещений; ТОП10 ресурсов CPU / GPU / память / диск / IO / IOPS в реальном времени, мгновенное обнаружение хостов с узкими местами.</td>
    <td align="center"><b>Управление хостами</b><br/><br/><img src="../image/2-zhuji.png" alt="Управление хостами" width="100%"><br/>Левое дерево активов, сгруппированное по дата-центру / бизнесу, правое отображение в карточках с метриками реального времени каждого хоста: CPU, память, подкачка, разделы диска, нагрузка 1/5/15 мин, пропускная способность сети, IOPS, количество процессов и соединений, поддерживает двойной вид сетка / список.</td>
  </tr>
  <tr>
    <td align="center"><b>Веб-терминал</b><br/><br/><img src="../image/3-zhongduan.png" alt="Веб-терминал" width="100%"><br/>Прямое подключение к целевым хостам через обратный канал Agent, без необходимости открывать входящие порты SSH. Поддержка мульти-вкладок для подключения к нескольким хостам, аудит команд и воспроизведение записей, режим наблюдателя.</td>
    <td align="center"><b>Удалённый рабочий стол</b><br/><br/><img src="../image/4-zhuomian.png" alt="Удалённый рабочий стол" width="100%"><br/>Удалённый рабочий стол с двойным кодированием JPEG / H.264, поддерживает переключение между несколькими экранами, адаптивное разрешение, системные сочетания клавиш, такие как Ctrl+Alt+Del; правая панель предоставляет загрузку/скачивание файлов и синхронизацию буфера обмена, опыт работы, близкий к локальному рабочему столу.</td>
  </tr>
  <tr>
    <td align="center"><b>Установка Agent</b><br/><br/><img src="../image/5-agent.png" alt="Установка Agent" width="100%"><br/>Одна команда для развёртывания Agent, поддерживает Linux / Windows / macOS три платформы. Опционально стандартный режим, режим ретрансляции шлюза, режим push на несколько серверов; стратегия Token и стратегия автообновления управляются централизованно в консоли.</td>
    <td align="center"><b>Мониторинг оборудования</b><br/><br/><img src="../image/6-jiqi.png" alt="Мониторинг оборудования" width="100%"><br/>Внеполосный сбор статуса оборудования физических серверов через Redfish / BMC / iDRAC / iLO: производитель, модель, серийный номер, питание/температура/потребление энергии, версия BIOS; журналы событий BMC (SEL) полностью сохраняются, поддерживает ИИ-диагностику.</td>
  </tr>
  <tr>
    <td align="center"><b>Управление контейнерами</b><br/><br/><img src="../image/7-docker.png" alt="Управление контейнерами" width="100%"><br/>Централизованное управление контейнерами Docker / Podman и проектами Compose на хостах: статус в реальном времени, сопоставление портов, информация об образах в одном взгляде; поддерживает запуск/остановку одним щелчком, перезапуск, просмотр журналов, пакетную фильтрацию между хостами.</td>
    <td align="center"><b>Оркестрация Playbook</b><br/><br/><img src="../image/8-juben.png" alt="Оркестрация Playbook" width="100%"><br/>Визуальные автоматизированные Playbook: проверка системы, проверка сети, проверка безопасности, перезапуск служб systemd, скользящий перезапуск K8s Deployment, глубокая проверка хостов, проверка Java-приложений/анализ производительности/анализ исключений и другие встроенные Playbook, готовые к использованию, поддерживает настраиваемую многошаговую параллельность и защитные ограждения утверждения.</td>
  </tr>
  <tr>
    <td align="center"><b>Центр SRE</b><br/><br/><img src="../image/9-sre.png" alt="Центр SRE" width="100%"><br/>Триггеры оповещений / выгорание SLO / вручную созданные события сходятся здесь, с полной временной шкалой и записями автоматического восстановления. Поддерживает восемь подмодулей: инциденты, автоматическое восстановление, топология зависимостей, SLO, тикеты, On-call, изменения, проверка здоровья платформы.</td>
    <td align="center"><b>ИИ-диагностика</b><br/><br/><img src="../image/10-ai.png" alt="ИИ-диагностика" width="100%"><br/>ИИ-ассистент одним щелчком в списке событий SRE, автоматически анализирует корневую причину текущего оповещения и даёт предложения по обработке. ИИ просматривает корреляции оповещений, извлекает похожие случаи, проверяет состояние здоровья критических хостов, процесс мышления полностью виден.</td>
  </tr>
  <tr>
    <td align="center"><b>Настройки оповещений</b><br/><br/><img src="../image/11-setting.png" alt="Настройки оповещений" width="100%"><br/>Многоканальная конфигурация push-оповещений: Feishu, DingTalk, Webhook, электронная почта, SMS, телефон шесть каналов на выбор; поддерживает стратегии тишины / подавления / маршрутизации, критические идут на телефон SMS, предупреждения идут в IM, избегает штормов оповещений.</td>
    <td align="center"><b>Настройки ИИ</b><br/><br/><img src="../image/12-aiset.png" alt="Настройки ИИ" width="100%"><br/>Комплексная настройка возможностей ИИ: диалоговые модели (совместимые с OpenAI / Bailian / DeepSeek / Ollama / Anthropic / Claude), векторная библиотека RAG, оценка и стоимость (MoA / цена за единицу), интеграция MCP, наблюдение за вызовами, авторизация безопасности шесть настроек, поддерживает голосовой ввод/трансляцию.</td>
  </tr>
</table>

### Мобильное приложение (Android / HarmonyOS)

> **Примечание**: Мобильное приложение (Android / HarmonyOS) является отдельным дистрибутивным пакетом, **версия сообщества с открытым исходным кодом не предоставляет пакеты установки приложения**. Если вам нужно использовать мобильное приложение, пожалуйста, свяжитесь с командой проекта.

<table>
  <tr>
    <td align="center"><b>Кокпит SRE</b><br/><br/><img src="../image/app01.jpg" alt="Кокпит SRE" width="100%"><br/>Мобильная обзорная страница: процент хостов онлайн, количество серьёзных/предупреждающих оповещений в одном взгляде; быстрые входы охватывают мониторинг оборудования, виртуальные машины, сетевой трафик, тесты дозвона, мониторинг хостов, поиск журналов, оркестрацию операций, панели мониторинга; ожидающие инциденты отсортированы по приоритету.</td>
    <td align="center"><b>Мониторинг инфраструктуры</b><br/><br/><img src="../image/app02.jpg" alt="Мониторинг инфраструктуры" width="100%"><br/>Мобильная страница инфраструктуры: четыре измерения хост/ресурс/сеть/тест дозвона переключаемые; обзор ресурсов GPU (модель, VRAM, температура); список хостов фильтруется по группе, реальное время отображения основных метрик CPU, памяти, диска.</td>
  </tr>
  <tr>
    <td align="center"><b>Мобильный терминал</b><br/><br/><img src="../image/app03.jpg" alt="Мобильный терминал" width="100%"><br/>Мобильный веб-терминал: прямое подключение к целевым хостам через обратный канал Agent, полный интерактивный опыт терминала; поддерживает сочетания клавиш, масштабирование шрифта, вращение экрана, устранение неполадок в любое время в любом месте.</td>
    <td align="center"><b>ИИ-ассистент эксплуатации</b><br/><br/><img src="../image/app04.jpg" alt="ИИ-ассистент эксплуатации" width="100%"><br/>Мобильный ИИ-диалог: опишите проблемы на естественном языке, ИИ автоматически извлекает исторические случаи, получает детали оповещений, проверяет состояние здоровья хоста, даёт анализ корневой причины и предложения по обработке; нижняя панель навигации охватывает пять основных входов обзор/мониторинг/оповещения/операции/ИИ.</td>
  </tr>
</table>

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
