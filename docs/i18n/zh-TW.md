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

## 📸 產品截圖

### Web 控制台

<table>
  <tr>
    <td align="center"><b>概覽儀表盤</b><br/><br/><img src="../../image/1-shouye.png" alt="概覽儀表盤" width="100%"><br/>叢集資源、告警與活動的統一視圖：主機在線率、系統健康狀態、活躍告警一覽；CPU / GPU / 記憶體 / 磁碟 / IO / IOPS 資源 TOP10 即時排行，一眼定位瓶頸主機。</td>
    <td align="center"><b>主機管理</b><br/><br/><img src="../../image/2-zhuji.png" alt="主機管理" width="100%"><br/>左側資產樹按機房 / 業務分組，右側卡片式展示每台主機的即時指標——CPU、記憶體、交換分割區、各磁碟分割區、1/5/15 分鐘負載、網路吞吐、IOPS、處理程序與連線數，支援網格 / 清單雙視圖。</td>
  </tr>
  <tr>
    <td align="center"><b>Web 遠端終端</b><br/><br/><img src="../../image/3-zhongduan.png" alt="Web 遠端終端" width="100%"><br/>經 Agent 反向通道直連目標主機，無需開啟 SSH 入站埠。支援多標籤頁同時連線多台主機、命令審計與錄製回放、旁觀模式。</td>
    <td align="center"><b>遠端桌面</b><br/><br/><img src="../../image/4-zhuomian.png" alt="遠端桌面" width="100%"><br/>JPEG / H.264 雙編碼遠端桌面，支援多屏切換、解析度自適應、Ctrl+Alt+Del 等系統快速鍵；右側面板提供檔案上傳/下載與剪貼簿同步，操作體驗接近本機桌面。</td>
  </tr>
  <tr>
    <td align="center"><b>Agent 安裝</b><br/><br/><img src="../../image/5-agent.png" alt="Agent 安裝" width="100%"><br/>一條命令完成 Agent 部署，支援 Linux / Windows / macOS 三平台。可選標準模式、閘道中繼模式、多服務端推送模式；Token 策略與自動更新策略均可在控制台統一管理。</td>
    <td align="center"><b>硬體資源監控</b><br/><br/><img src="../../image/6-jiqi.png" alt="硬體資源監控" width="100%"><br/>透過 Redfish / BMC / iDRAC / iLO 帶外採集實體伺服器硬體狀態：廠商、型號、序號、電源/溫度/功耗、BIOS 版本；BMC 事件日誌（SEL）完整留存，支援 AI 診斷。</td>
  </tr>
  <tr>
    <td align="center"><b>容器管理</b><br/><br/><img src="../../image/7-docker.png" alt="容器管理" width="100%"><br/>統一管理主機上的 Docker / Podman 容器與 Compose 專案：即時狀態、埠映射、映像資訊一目瞭然；支援一鍵啟停、重啟、日誌檢視，跨主機批次篩選。</td>
    <td align="center"><b>劇本編排</b><br/><br/><img src="../../image/8-juben.png" alt="劇本編排" width="100%"><br/>視覺化自動化運維劇本：系統巡檢、網路巡檢、安全巡檢、systemd 服務重啟、K8s Deployment 滾動重啟、深度主機巡檢、Java 應用巡檢/效能分析/異常分析等內建劇本開箱即用，支援自訂多步驟平行與審批護欄。</td>
  </tr>
  <tr>
    <td align="center"><b>SRE 中樞</b><br/><br/><img src="../../image/9-sre.png" alt="SRE 中樞" width="100%"><br/>告警觸發 / SLO 燃盡 / 手動建立的事件統一匯聚於此，含完整時間線與自動修復記錄。支援事件、自動修復、依賴拓撲、SLO、工單、On-call、變更、平台健康巡檢八大子模組。</td>
    <td align="center"><b>AI 診斷</b><br/><br/><img src="../../image/10-ai.png" alt="AI 診斷" width="100%"><br/>在 SRE 事件清單中一鍵喚起 AI 助手，自動分析當前告警根因並給出處置建議。AI 會梳理告警關聯、檢索相似案例、檢查關鍵主機健康狀態，思考過程全程可見。</td>
  </tr>
  <tr>
    <td align="center"><b>告警設定</b><br/><br/><img src="../../image/11-setting.png" alt="告警設定" width="100%"><br/>多管道告警推送配置：飛書、釘釘、Webhook、郵件、簡訊、電話六通道可選；支援靜默 / 抑制 / 路由策略，危急走電話簡訊、警告走 IM，避免告警風暴。</td>
    <td align="center"><b>AI 設定</b><br/><br/><img src="../../image/12-aiset.png" alt="AI 設定" width="100%"><br/>AI 能力一站式配置：對話模型（OpenAI 相容 / 百煉 / DeepSeek / Ollama / Anthropic / Claude）、RAG 向量庫、研判與成本（MoA / 單價）、MCP 整合、呼叫觀測、安全授權六項設定，支援語音輸入/播報。</td>
  </tr>
</table>

### 行動端 App（Android / HarmonyOS）

> **說明**：行動端 App（Android / HarmonyOS）為獨立分發包，**開源社群版本不提供 App 安裝包**。如需使用行動端，請聯絡專案方獲取。

<table>
  <tr>
    <td align="center"><b>SRE 駕駛艙</b><br/><br/><img src="../../image/app01.jpg" alt="SRE 駕駛艙" width="100%"><br/>行動端總覽頁：主機在線率、嚴重/警告告警數一目瞭然；捷徑入口覆蓋硬體監控、虛擬機器、網路流量、撥測、主機監控、日誌檢索、運維編排、儀表盤；待處理事件按優先順序排列。</td>
    <td align="center"><b>基礎設施監控</b><br/><br/><img src="../../image/app02.jpg" alt="基礎設施監控" width="100%"><br/>行動端基礎設施頁：主機/資源/網路/撥測四大維度切換；GPU 資源概覽（型號、顯存、溫度）；主機清單按分組篩選，即時展示 CPU、記憶體、磁碟等核心指標。</td>
  </tr>
  <tr>
    <td align="center"><b>行動端遠端終端</b><br/><br/><img src="../../image/app03.jpg" alt="行動端遠端終端" width="100%"><br/>行動端 Web 終端：經 Agent 反向通道直連目標主機，完整終端互動體驗；支援快速鍵、字體縮放、螢幕旋轉，隨時隨地排查問題。</td>
    <td align="center"><b>AI 運維助手</b><br/><br/><img src="../../image/app04.jpg" alt="AI 運維助手" width="100%"><br/>行動端 AI 對話：自然語言描述問題，AI 自動檢索歷史案例、拉取告警詳情、檢查主機健康狀態，給出根因分析與處置建議；底部導覽列覆蓋總覽/監控/告警/運維/AI 五大入口。</td>
  </tr>
</table>

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
