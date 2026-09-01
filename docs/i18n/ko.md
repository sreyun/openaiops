<div align="center">

# AIOps

**오픈소스 셀프호스팅 호스트 모니터링 & SRE 플랫폼**  
관측 · 알림 · 자동복구 · 원격 운영 · Agent OTA · AI 진단 — 완전히 통제하는 하나의 바이너리로.

[![Version](https://img.shields.io/badge/Version-v0.20.49-blue)](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](../../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Android%20%7C%20HarmonyOS-lightgrey)]()
[![Stars](https://img.shields.io/github/stars/sreyun/openaiops?style=social)](https://github.com/sreyun/openaiops)

**[简体中文](../../README.md) · [繁體中文](zh-TW.md) · [English](en.md) · [日本語](ja.md) · [한국어](ko.md) · [Français](fr.md) · [Deutsch](de.md) · [Español](es.md) · [Português](pt-BR.md) · [Русский](ru.md)**

[빠른 시작](#-빠른-시작) · [핵심 기능](#-핵심-기능) · [문서](../README.md) · [변경 로그](../../CHANGELOG.md) · [Releases](https://github.com/sreyun/openaiops/releases)

</div>

---

## 왜 AIOps인가

모니터링·알림·Bastion·런북이 따로 늘고, 상용은 호스트 과금에 데이터는 클라우드에 남습니다.

AIOps는 자주 쓰는 경로를 **하나의 셀프호스팅 플랫폼**으로 모읍니다:

| | AIOps | 전형적인 조합 스택 |
|---|---|---|
| **구성** | Go 서버 1 + 의존성 없는 Agent 1 | Zabbix / Prometheus / Grafana / Alertmanager / Bastion / 런북… |
| **도입** | `docker compose up -d`（약 3분） | 연동에 수일 |
| **데이터** | PostgreSQL + VictoriaMetrics（**자체 보유**） | SaaS 또는 분산 DB |
| **원격** | 웹 터미널／데스크톱／포트 포워드, Agent **아웃바운드만** | 별도 VPN／Bastion |
| **플릿** | **Agent OTA 자동 업그레이드**（SHA-256 검증, 유지보수 창, 일괄 push, 롤백） | 호스트별 SSH 바이너리 교체 |
| **루프** | 알림 → 플레이북 → 인시던트／SLO／티켓 → AI RCA | 사람이 이어 붙임 |
| **라이선스** | **AGPL-3.0**, 호스트 수 제한 없음 | 노드／모듈 과금 |

> 프라이빗 DC·하이브리드 클라우드, 가시성·통제·변경 안전·설명 가능한 운영이 필요한 팀용.

---

## ✨ 핵심 기능

기능 나열이 아닌 일곱 기둥：

```
  Observe ──────► Govern ──────► Remediate ──────► Diagnose
  Hosts/GPU/logs   Silence/route   Playbooks/gates   AI · RAG · MCP
  Probes/OOB       Multi-channel   Incident/SLO      Evidence gate

  Remote · terminal/desktop/forward (reverse tunnel)   Fleet · Agent OTA
  Security · RBAC/MFA/FIM
```

1. **관측** — 크로스 플랫폼 Agent(Linux / Windows / macOS / Kylin), GPU, 로그, HTTP/TCP 프로브, API SLI, Redfish / SNMP / NetFlow / 컨테이너 / K8s / Hyper-V.
2. **거버넌스** — 임계값 프리셋, silence / inhibit / route; Feishu / DingTalk / 메일 / SMS / 음성.
3. **복구 & SRE** — 승인 가드레일 플레이북; 인시던트, SLO, 티켓, 동결 창, 감사 가능한 break-glass.
4. **AI 진단** — 점검 + RCA(OpenAI 호환, 미설정 시 휴리스틱); pgvector RAG, Skills, MCP(Cursor / Claude); 음성 자가 테스트.
5. **원격 운영** — 웹 터미널(재생 / 관전 / 감사 / 2차 비밀번호), 원격 데스크톱(JPEG/H.264), 포트 포워드 / HTTP 프록시와 SSRF 방어.
6. **안전한 제공** — RBAC, MFA, Agent 지문, AES-256-GCM; Web 콘솔; Android / HarmonyOS 별도 배포。
7. **Agent OTA** — 서버 업그레이드 후 뒤처진 온라인 Agent 자동 큐(기본 ON); 콘솔 일괄 push 또는 `POST /api/v1/agents/update`; `/dl/` SHA-256 검증, `.bak` 롤백.

현재 릴리스 **[v0.20.49](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)** · [GitHub](https://github.com/sreyun/openaiops)／[Gitee](https://gitee.com/bigdatasafe/openaiops)

---

## 🚀 빠른 시작

> 서버는 PostgreSQL과 VictoriaMetrics **둘 다 필수**입니다.

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

설치 상세 → **[../getting-started/install.en.md](../getting-started/install.en.md)** · 운영 → **[../getting-started/deploy.en.md](../getting-started/deploy.en.md)**

---

## 🏗 아키텍처

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

## 📚 문서

장문과 다국어 README는 [`docs/`](../README.md)에 모았습니다. 루트에는 중국어 README와 CHANGELOG만 둡니다.

| Need | Doc |
|------|-----|
| Install | [../getting-started/install.md](../getting-started/install.md) · [EN](../getting-started/install.en.md) |
| Agent OTA soak | [../engineering/agent-update-soak.md](../engineering/agent-update-soak.md) |
| Production deploy | [../getting-started/deploy.md](../getting-started/deploy.md) · [EN](../getting-started/deploy.en.md) |
| End-user guide | [../guides/user-guide.md](../guides/user-guide.md) |
| Port forward | [../guides/forward.md](../guides/forward.md) |
| Content audit / playbooks | [../guides/content-audit.md](../guides/content-audit.md) |
| CI / SQL gates | [../engineering/ci-gate.md](../engineering/ci-gate.md) |

---

## 🤝 기여

Issue／PR／번역을 환영합니다. 권장: `make build` · `make audit`.

AIOps가 조합 스택을 대체했다면 **Star** 부탁드립니다.

---

## 라이선스

[AGPL-3.0](../../LICENSE). 호스트 수 제한 없음. 모바일은 별도 패키지(본 저장소에 소스 없음).

---

<p align="center">
  <b>AIOps · 운영 복잡도를, 직접 소유하는 플랫폼으로.</b><br/>
  <sub>Star ⭐ · Fork · Issue · 셀프호스팅 운영을 함께</sub>
</p>
