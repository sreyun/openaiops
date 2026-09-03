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

## 📸 제품 스크린샷

### Web 콘솔

<table>
  <tr>
    <td align="center"><b>개요 대시보드</b><br/><br/><img src="../../image/1-shouye.png" alt="개요 대시보드" width="100%"><br/>클러스터 리소스, 알림 및 활동의 통합 뷰: 호스트 온라인율, 시스템 건강 상태, 활성 알림 개요; CPU / GPU / 메모리 / 디스크 / IO / IOPS 리소스 TOP10 실시간 랭킹, 병목 호스트를 한눈에 파악.</td>
    <td align="center"><b>호스트 관리</b><br/><br/><img src="../../image/2-zhuji.png" alt="호스트 관리" width="100%"><br/>왼쪽 자산 트리는 데이터센터 / 업무별로 그룹화, 오른쪽 카드 스타일 표시는 각 호스트의 실시간 메트릭스를 표시—CPU, 메모리, 스왑, 각 디스크 파티션, 1/5/15 분 부하, 네트워크 처리량, IOPS, 프로세스 및 연결 수, 그리드 / 리스트 이중 뷰 지원.</td>
  </tr>
  <tr>
    <td align="center"><b>Web 터미널</b><br/><br/><img src="../../image/3-zhongduan.png" alt="Web 터미널" width="100%"><br/>Agent 리버스 채널을 통해 대상 호스트에 직접 연결, SSH 인바운드 포트를 열 필요 없음. 멀티탭으로 여러 호스트에 동시 연결, 명령 감사 및 녹화 재생, 옵저버 모드 지원.</td>
    <td align="center"><b>원격 데스크톱</b><br/><br/><img src="../../image/4-zhuomian.png" alt="원격 데스크톱" width="100%"><br/>JPEG / H.264 이중 인코딩 원격 데스크톱, 멀티 화면 전환, 해상도 자동 적응, Ctrl+Alt+Del 등 시스템 단축키 지원; 오른쪽 패널은 파일 업로드/다운로드 및 클립보드 동기화 제공, 로컬 데스크톱에 가까운 작업 경험.</td>
  </tr>
  <tr>
    <td align="center"><b>Agent 설치</b><br/><br/><img src="../../image/5-agent.png" alt="Agent 설치" width="100%"><br/>하나의 명령으로 Agent 배포, Linux / Windows / macOS 3개 플랫폼 지원. 표준 모드, 게이트웨이 릴레이 모드, 다중 서버 푸시 모드 선택 가능; Token 전략 및 자동 업데이트 전략은 콘솔에서 통합 관리.</td>
    <td align="center"><b>하드웨어 리소스 모니터링</b><br/><br/><img src="../../image/6-jiqi.png" alt="하드웨어 리소스 모니터링" width="100%"><br/>Redfish / BMC / iDRAC / iLO를 통해 물리 서버의 하드웨어 상태를 대역외 수집: 벤더, 모델, 시리얼 번호, 전원/온도/전력 소비, BIOS 버전; BMC 이벤트 로그(SEL)를 완전히 보존, AI 진단 지원.</td>
  </tr>
  <tr>
    <td align="center"><b>컨테이너 관리</b><br/><br/><img src="../../image/7-docker.png" alt="컨테이너 관리" width="100%"><br/>호스트의 Docker / Podman 컨테이너 및 Compose 프로젝트를 통합 관리: 실시간 상태, 포트 매핑, 이미지 정보를 한눈에; 원클릭 시작/중지, 재시작, 로그 보기, 크로스 호스트 일괄 필터링 지원.</td>
    <td align="center"><b>Playbook 오케스트레이션</b><br/><br/><img src="../../image/8-juben.png" alt="Playbook 오케스트레이션" width="100%"><br/>시각화 자동화 운영 Playbook: 시스템 점검, 네트워크 점검, 보안 점검, systemd 서비스 재시작, K8s Deployment 롤링 재시작, 딥 호스트 점검, Java 애플리케이션 점검/성능 분석/예외 분석 등 내장 Playbook을 바로 사용 가능, 커스텀 다중 단계 병렬 및 승인 가드레일 지원.</td>
  </tr>
  <tr>
    <td align="center"><b>SRE 허브</b><br/><br/><img src="../../image/9-sre.png" alt="SRE 허브" width="100%"><br/>알림 트리거 / SLO 번다운 / 수동 생성된 이벤트가 여기에 집약, 완전한 타임라인과 자동 복구 기록 포함. 8개 서브모듈 지원: 인시던트, 자동 복구, 의존성 토폴로지, SLO, 티켓, On-call, 변경, 플랫폼 건강 점검.</td>
    <td align="center"><b>AI 진단</b><br/><br/><img src="../../image/10-ai.png" alt="AI 진단" width="100%"><br/>SRE 이벤트 목록에서 원클릭 AI 어시스턴트 호출, 현재 알림의 근본 원인을 자동 분석하고 처리 제안 제공. AI는 알림 상관관계를 정리하고, 유사 사례를 검색하고, 중요 호스트 건강 상태를 확인하며, 사고 과정이 완전히_visible.</td>
  </tr>
  <tr>
    <td align="center"><b>알림 설정</b><br/><br/><img src="../../image/11-setting.png" alt="알림 설정" width="100%"><br/>다중 채널 알림 푸시 구성: Feishu, DingTalk, Webhook, 이메일, SMS, 전화 6개 채널 선택 가능; 침묵 / 억제 / 라우팅 전략 지원, 중요도는 전화 SMS, 경고는 IM, 알림 폭풍 방지.</td>
    <td align="center"><b>AI 설정</b><br/><br/><img src="../../image/12-aiset.png" alt="AI 설정" width="100%"><br/>원스톱 AI 기능 구성: 대화 모델(OpenAI 호환 / 百煉 / DeepSeek / Ollama / Anthropic / Claude), RAG 벡터 라이브러리, 판단 및 비용(MoA / 단가), MCP 통합, 호출 관측, 보안 인증 6개 항목 설정, 음성 입력/방송 지원.</td>
  </tr>
</table>

### 모바일 App (Android / HarmonyOS)

> **참고**: 모바일 App(Android / HarmonyOS)은 독립 배포 패키지이며, **오픈소스 커뮤니티 버전에서는 App 설치 패키지를 제공하지 않습니다**. 모바일 사용을 원하시면 프로젝트 팀에 문의해 주세요.

<table>
  <tr>
    <td align="center"><b>SRE 콕핏</b><br/><br/><img src="../../image/app01.jpg" alt="SRE 콕핏" width="100%"><br/>모바일 개요 페이지: 호스트 온라인율, 심각/경고 알림 수가 한눈에; 빠른 진입은 하드웨어 모니터링, 가상 머신, 네트워크 트래픽, dial 테스트, 호스트 모니터링, 로그 검색, 운영 오케스트레이션, 대시보드를 커버; 보류 중인 인시던트는 우선도별로 정렬.</td>
    <td align="center"><b>인프라 모니터링</b><br/><br/><img src="../../image/app02.jpg" alt="인프라 모니터링" width="100%"><br/>모바일 인프라 페이지: 호스트/리소스/네트워크/dial 테스트 4개 차원 전환; GPU 리소스 개요(모델, VRAM, 온도); 호스트 리스트는 그룹별로 필터링, CPU, 메모리, 디스크 등 핵심 메트릭스를 실시간 표시.</td>
  </tr>
  <tr>
    <td align="center"><b>모바일 터미널</b><br/><br/><img src="../../image/app03.jpg" alt="모바일 터미널" width="100%"><br/>모바일 Web 터미널: Agent 리버스 채널을 통해 대상 호스트에 직접 연결, 완전한 터미널 인터랙티브 경험; 단축키, 글꼥 스케일링, 화면 회전 지원, 언제 어디서나 문제排查.</td>
    <td align="center"><b>AI 운영 어시스턴트</b><br/><br/><img src="../../image/app04.jpg" alt="AI 운영 어시스턴트" width="100%"><br/>모바일 AI 대화: 자연어로 문제를 설명하면, AI가 자동으로 역사 사례를 검색하고, 알림 세부 정보를 가져오고, 호스트 건강 상태를 확인하고, 근본 원인 분석과 처리 제안을 제공; 하단 탐색 바는 개요/모니터링/알림/운영/AI 5개 주요 진입점을 커버.</td>
  </tr>
</table>

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
