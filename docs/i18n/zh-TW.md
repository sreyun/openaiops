<div align="center">

# AIOps

**開源、可私有化的主機監控與 SRE 運維平台**  
觀測 · 告警 · 自癒 · 遠端運維 · Agent OTA · AI 診斷 —— 收斂進一個你完全掌控的二進位。

[![Version](https://img.shields.io/badge/Version-v0.20.49-blue)](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](../../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Android%20%7C%20HarmonyOS-lightgrey)]()
[![Stars](https://img.shields.io/github/stars/sreyun/openaiops?style=social)](https://github.com/sreyun/openaiops)

**[简体中文](../../README.md) · [繁體中文](zh-TW.md) · [English](en.md) · [日本語](ja.md) · [한국어](ko.md) · [Français](fr.md) · [Deutsch](de.md) · [Español](es.md) · [Português](pt-BR.md) · [Русский](ru.md)**

[快速開始](#-快速開始) · [核心能力](#-核心能力) · [文件中心](../README.md) · [變更日誌](../../CHANGELOG.md) · [Releases](https://github.com/sreyun/openaiops/releases)

</div>

---

## 為什麼選 AIOps

運維工具越堆越多：監控一套、告警一套、終端一套、劇本又一套；商業產品還按主機／模組計費，資料卻在別人的雲上。

AIOps 把高頻路徑收斂為 **一個可自託管的平台**：

| | AIOps | 典型拼裝棧 |
|---|---|---|
| **元件** | 1 個 Go 服務端 + 1 個零依賴 Agent | Zabbix / Prometheus / Grafana / Alertmanager / 堡壘機 / 劇本系統… |
| **上線** | `docker compose up -d`，約 3 分鐘 | 多元件聯調，常以天計 |
| **資料** | PostgreSQL + VictoriaMetrics，**永久自持** | SaaS 或分散多庫 |
| **遠端** | Web 終端／桌面／埠轉發，Agent **反向連線**免開入站 | 另購堡壘機或 VPN |
| **機群** | **Agent OTA 自動升級**（SHA-256 校驗、維護窗、批量推送、失敗回滾） | 逐台 SSH 替換二進位 |
| **閉環** | 告警 → 劇本／自癒 → 事件／SLO／工單 → AI 研判 | 工具之間靠人肉黏合 |
| **授權** | **AGPL-3.0**，無主機數閹割 | 按節點／模組收費 |

> 適合：自建機房、混合雲、信創環境；需要「看得見、管得住、改得動、說得清」的運維與 SRE 團隊。

---

## ✨ 核心能力

圍繞七條主線，而不是功能清單堆砌：

```
  Observe ──────► Govern ──────► Remediate ──────► Diagnose
  Hosts/GPU/logs   Silence/route   Playbooks/gates   AI · RAG · MCP
  Probes/OOB       Multi-channel   Incident/SLO      Evidence gate

  Remote · terminal/desktop/forward (reverse tunnel)   Fleet · Agent OTA
  Security · RBAC/MFA/FIM
```

1. **觀測** — 跨平台原生 Agent（Linux／Windows／macOS／麒麟）、GPU、日誌、HTTP／TCP 撥測、API SLI、Redfish／SNMP／NetFlow／容器／K8s／Hyper-V。
2. **治理** — 閾值檔位 + 靜默／抑制／路由；飛書／釘釘／郵件／簡訊／語音。
3. **自癒與 SRE** — 劇本審批護欄；事件、SLO、工單、凍結窗、可審計 break-glass。
4. **AI 診斷** — 巡檢與根因（OpenAI 相容模型，未設定時啟發式）；pgvector RAG、Skills、MCP（Cursor／Claude）；語音自測。
5. **遠端運維** — Web 終端（回放／旁觀／審計／二次密碼）、遠端桌面（JPEG／H.264）、埠轉發／HTTP 代理與 SSRF 防護。
6. **安全與交付** — RBAC、MFA、Agent 指紋、AES-256-GCM；Android／HarmonyOS 獨立分發。
7. **Agent OTA** — 服務端升級後，落後的線上 Agent 預設自動入隊；也可在控制台批量推送或呼叫 `POST /api/v1/agents/update`；從 `/dl/` 下載並 SHA-256 校驗，失敗回滾 `.bak`；支援維護窗、豁免名單與跳過原因查詢。

目前版本 **[v0.20.49](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)** · 鏡像 [GitHub](https://github.com/sreyun/openaiops)／[Gitee](https://gitee.com/bigdatasafe/openaiops)

---

## 🚀 快速開始

> 服務端**強制依賴** PostgreSQL 與 VictoriaMetrics。

```bash
bash scripts/secure-compose.sh   # 生成 .env 強隨機密鑰
docker compose up -d
# 開啟 http://localhost:8529 → admin / admin（首次登入強制改密）
# 從控制台生成 Agent 安裝指令；服務端升級後 Agent 預設自動 OTA
```

```bash
export AIOPS_POSTGRES_DSN="postgres://aiops:secret@127.0.0.1:5432/aiops?sslmode=disable"
export AIOPS_VM_URL="http://127.0.0.1:8428"
./aiops-server

go build ./cmd/server ./cmd/agent   # Go 1.26+
```

完整安裝 → **[../getting-started/install.md](../getting-started/install.md)** · 生產部署 → **[../getting-started/deploy.md](../getting-started/deploy.md)**

---

## 🏗 架構一覽

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

## 📚 文件中心

長文與多語言簡介在 [`docs/`](../README.md)；根目錄僅保留簡體中文 README 與變更日誌。

| 用途 | 文件 |
|------|------|
| 安裝 | [../getting-started/install.md](../getting-started/install.md) · [EN](../getting-started/install.en.md) |
| 生產部署 | [../getting-started/deploy.md](../getting-started/deploy.md) · [EN](../getting-started/deploy.en.md) |
| Agent OTA 浸泡驗收 | [../engineering/agent-update-soak.md](../engineering/agent-update-soak.md) |
| 使用指南 | [../guides/user-guide.md](../guides/user-guide.md) |
| 埠轉發 | [../guides/forward.md](../guides/forward.md) |
| 內容審計與劇本 | [../guides/content-audit.md](../guides/content-audit.md) |
| CI / SQL 門禁 | [../engineering/ci-gate.md](../engineering/ci-gate.md) |

---

## 🤝 貢獻與社群

歡迎 Issue／PR／翻譯。建議：`make build` · `make audit`。

若 AIOps 幫你省下一套拼裝棧，**請點一下 Star** —— 這是對開源維護最直接的支持。

---

## 授權條款

[AGPL-3.0](../../LICENSE)。無主機數限制、無功能閹割套路。行動端為獨立分發包，原始碼不在本倉庫。

---

<p align="center">
  <b>AIOps · 把運維的複雜度，收斂進一個你完全掌控的平台。</b><br/>
  <sub>Star ⭐ · Fork · 提 Issue · 一起把自託管運維做紮實</sub>
</p>
