<div align="center">

# AIOps

**인바운드 포트 없이 쓰는 셀프호스트 운영 콘솔: 호스트를 보고 · 터미널/데스크톱을 열고 · 알림을 다룹니다**

[![Version](https://img.shields.io/badge/Version-v1.0.6-blue)](https://github.com/sreyun/aiops/releases/tag/v1.0.6)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS-lightgrey)]()

**[简体中文](README.md) · [繁體中文](README.zh-TW.md) · [English](README_EN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt-BR.md) · [Русский](README.ru.md)**

</div>

> 많은 서버는 NAT / 방화벽 뒤에 있어 Agent는 설치해도 인바운드를 열기 어렵습니다.  
> AIOps는 **역방향 연결 Agent**로 모니터링 + 웹 터미널/데스크톱 + 알림을 하나의 셀프호스트 제어면에 모읍니다. 서버는 `docker compose`, 대상 호스트는 설치 명령 한 줄이면 됩니다.

**현재 릴리스 [v1.0.6](https://github.com/sreyun/aiops/releases/tag/v1.0.6)** · [GitHub](https://github.com/sreyun/aiops) / [Gitee](https://gitee.com/bigdatasafe/aiops) · [CHANGELOG](CHANGELOG.md)

---

## 먼저 이 경로（약 3분）

```bash
docker compose up -d
open http://localhost:8529
# UI의 「설치 명령」을 대상 호스트에서 실행（Agent가 아웃바운드로 연결, 인바운드 불필요）
# curl -fsSL "http://<server>:8529/install.sh?token=<TOKEN>" | sudo sh
```

바로 아래 세 가지를 확인하세요:

1. **호스트 목록에 온라인이 뜬다**（CPU / 메모리 / 디스크 수집）  
2. **웹 터미널이 열린다**  
3. **임계값 알림 1개**로 Feishu / DingTalk / 메일이 온다  

이것이 핵심 시나리오: **역방향 연결 운영 콘솔**입니다. 나머지 기능은 이 위에 쌓입니다.

---

## 왜 AIOps인가

| | |
|---|---|
| **역방향 연결, 네트워크 변경 최소** | Agent가 Server로 아웃바운드. 터미널 / 데스크톱 / 포워딩도 같은 터널 |
| **단일 바이너리 + 의존성 없는 Agent** | 서버는 Go 하나. Agent는 표준 라이브러리 수집（Linux / Windows / macOS / Kylin） |
| **데이터는 당신 것** | PostgreSQL + VictoriaMetrics, MIT, 기능 제한 없음 |

> 「생존 체크만」「대시보드만」하는 작은 도구가 아닙니다. 중소팀이 흔히 이어 붙이는 모니터링 + 알림 + 원격 장애 처리를 하나의 플랫폼으로 모읍니다.  
> 기능은 넓어도 됩니다——**입구 설명은 좁게 유지하세요.**

---

## 목차

- [먼저 이 경로](#먼저-이-경로약-3분)
- [왜 AIOps인가](#왜-aiops인가)
- [기능 맵](#기능-맵)
- [최근 하이라이트](#최근-하이라이트)
- [설치](#설치)
- [권장 사용 경로](#권장-사용-경로)
- [설정 요점](#설정-요점)
- [아키텍처](#아키텍처)
- [문서와 경계](#문서와-경계)
- [기여](#기여)
- [라이선스](#라이선스)

---

## 기능 맵

주 경로를 먼저 통과하세요. 상세는 필요할 때 펼칩니다.

```
핵심 경로（먼저 여기）
  호스트 모니터링 → 알림 거버넌스 → 웹 터미널 / 원격 데스크톱 → 포트 포워딩

플랫폼 확장（같은 제어면）
  프로브 / API 모니터링 · 로그 · 플레이북 · SRE（인시던트 / SLO / 티켓）
  AI 점검 / Hermes / MCP · 보안 센터 · Hyper-V / 컨테이너 / K8s
  SNMP / NetFlow / Redfish · SQL 도구 · Android / HarmonyOS 콘솔*
```

\* 모바일은 별도 배포. 소스는 이 저장소에 없습니다.

<details>
<summary><b>호스트와 리소스 모니터링</b></summary>

- 네이티브 수집: CPU / 메모리 / 디스크 / 프로세스 / 포트 / 네트워크 / DiskIO / IOPS / GPU / 부하  
- 대역외（대상 Agent 불필요）: Redfish, NetFlow, Huawei OceanStor, SNMP, 컨테이너 / Hyper-V / K8s  
- 전역 검색과 토폴로지 보조  

</details>

<details>
<summary><b>알림 · 프로브 · 관측</b></summary>

- 임계값 + 사일런스 / 억제 / 라우트; Feishu, DingTalk, 메일, SMS, 음성  
- 프로브: Ping / TCP / HTTP / 프로세스; API 가용률 / P95 / 처리량  
- 로그 수집（암호화 업로드 가능）+ 전문 검색; 시계열은 VictoriaMetrics  

</details>

<details>
<summary><b>터미널 · 데스크톱 · 포워딩</b></summary>

- 웹 터미널: 탭, 녹화 재생, 읽기 전용, 명령 감사, 2차 비밀번호  
- 웹 원격 데스크톱: JPEG / H.264; Windows 잠금 화면은 **서비스 설치** 필요  
- 포트 포워드 / HTTP 리버스 프록시（WebSocket 포함）, SSRF 출구 보호  

</details>

<details>
<summary><b>자동화 · SRE · AI</b></summary>

- 플레이북, 승인 가드 자동 복구, 인시던트 / SLO / 티켓, 통합 수신함  
- AI 점검과 RCA（OpenAI 호환）; pgvector RAG; Hermes 대화  
- MCP: Cursor / Claude에 읽기 전용 도구 노출, 외부 MCP 연결 가능  

</details>

<details>
<summary><b>보안과 모바일</b></summary>

- RBAC, 선택 MFA, 감사, 머신 지문, 정적 암호화, 선택 TLS, 보안 센터  
- Android / HarmonyOS는 별도 배포; 푸시는 자체 장연결  

</details>

---

## 최근 하이라이트

| 영역 | 내용 |
|---|---|
| **듀얼 UI** | 클래식 + Vue 콘솔（`/v2/`） |
| **MCP** | Streamable HTTP 당직 / 진단 도구; 외부 MCP Clients |
| **Agent 원격 업데이트** | 일괄 배포; Windows는 버전 ACK 후 성공 |
| **데스크톱 강화** | Windows 잠금 / CAD / 잠금 해제 지속 개선（서비스 설치） |

전체: [CHANGELOG.md](CHANGELOG.md) · [Releases](https://github.com/sreyun/aiops/releases)

---

## 설치

> 서버는 **PostgreSQL과 VictoriaMetrics 둘 다 필수**입니다.

```bash
docker compose up -d
# 개발: docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build
```

`http://localhost:8529`에서 최초 보안 초기화（강제 비밀번호 변경）. 이후 MFA 권장.

선택적 강화 부트스트랩: [`scripts/secure-compose.sh`](scripts/secure-compose.sh)

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/sreyun/aiops/master/scripts/secure-compose.sh)
docker compose up -d
```

바이너리 / 소스:

```bash
export AIOPS_POSTGRES_DSN="postgres://aiops:secret@localhost:5432/aiops?sslmode=disable"
export AIOPS_VM_URL="http://localhost:8428"
./aiops-server
cp config.example.yaml config.yaml && ./aiops-agent --config config.yaml
# Go 1.26+: go build ./cmd/server ./cmd/agent
```

상세: [INSTALL.md](INSTALL.md) · [DEPLOY_GUIDE.md](DEPLOY_GUIDE.md) · 영어 [INSTALL_EN.md](INSTALL_EN.md)

---

## 권장 사용 경로

1. **호스트 등록** → 설치 명령 → 온라인 확인  
2. **모니터링 확인** → 호스트 상세; 필요 시 프로브 / API 모니터링  
3. **알림 수신** → 임계값과 거버넌스 → IM / 메일  
4. **원격 장애 처리** → 웹 터미널; Windows 데스크톱은 서비스 설치  
5. **확장** → 플레이북, SRE, AI / MCP, 보안, SNMP …

클래식 UI: `/` · Vue: `/v2/` 또는 `/?ui=v2`

---

## 설정 요점

| 변수 | 용도 | 필수 |
|---|---|---|
| `AIOPS_POSTGRES_DSN` | PostgreSQL | 예 |
| `AIOPS_VM_URL` | VictoriaMetrics | 예 |
| `AIOPS_LISTEN` | 리슨（기본 `:8529`） | 아니오 |
| `AIOPS_SECRET_KEY` | 설정 AES-GCM 암호화 | 운영 권장 |
| `AIOPS_TLS_CERT` / `AIOPS_TLS_KEY` | HTTPS | 운영 권장 |

Agent: `server` / `token` / `category`, 보고 간격, `servers[]`, 로그, 릴레이, Redfish / SNMP 등.  
예시: [config.example.yaml](config.example.yaml)

---

## 아키텍처

```
Browser / mobile* ──REST/WS──► Go server ──► PostgreSQL
                                  │           VictoriaMetrics
                                  ▲
                     역방향 연결 / 보고
                                  │
                            Go agent（메트릭 + 터미널/데스크톱 터널）
```

스토어 중 하나라도 없으면 기동 거부. 같은 Agent 면으로 대역외 수집·플러그인 확장 가능.

---

## 문서와 경계

| 문서 | 내용 |
|---|---|
| [USER_GUIDE.md](USER_GUIDE.md) | 사용 시나리오 |
| [INSTALL.md](INSTALL.md) / [DEPLOY_GUIDE.md](DEPLOY_GUIDE.md) | 설치 |
| [FORWARD_GUIDE.md](FORWARD_GUIDE.md) | 포트 포워딩 |
| [docs/year1-acceptance.md](docs/year1-acceptance.md) | POC 수락 |

**경계**: 단일 인스턴스 규모 상한; LLM 미설정 시 휴리스틱; Windows 잠금 화면 데스크톱은 서비스 설치 필수; 모바일은 별도 배포.

---

## 기여

Issue / PR / 문서 환영. 모듈이 많으므로 **핵심 경로 관련 변경부터** 시작하는 편이 리뷰에 유리합니다:

1. Agent 설치 / 보고 / 역채널  
2. 호스트 모니터링과 알림 UX  
3. 웹 터미널 / 데스크톱과 설치 문서  

개발: Go 1.26+; `make build` · `make audit`. 보안 이슈는 공개 Issue가 아닌 비공개 채널로.

| | |
|---|---|
| GitHub | <https://github.com/sreyun/aiops> |
| Gitee | <https://gitee.com/bigdatasafe/aiops> |
| Releases | <https://github.com/sreyun/aiops/releases> |

---

## 라이선스

**MIT** — [LICENSE](LICENSE). 호스트 수 제한·기능 제한·강제 텔레메트리 없음.  
`vendor/`는 각 라이선스를 따릅니다. 모바일 클라이언트는 별도 배포입니다.

---

<p align="center">
  <b>먼저 「Agent 설치 → 호스트 확인 → 터미널 열기」.<br/>나머지 능력은 당신이 완전히 통제하는 같은 플랫폼 안에 있습니다.</b>
</p>
