<div align="center">

# AIOps

**免開入站埠的自託管維運台：看得見主機 · 開得了終端/桌面 · 收得住告警**

[![Version](https://img.shields.io/badge/Version-v1.0.6-blue)](https://github.com/sreyun/aiops/releases/tag/v1.0.6)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS-lightgrey)]()

**[简体中文](README.md) · [繁體中文](README.zh-TW.md) · [English](README_EN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt-BR.md) · [Русский](README.ru.md)**

</div>

> 許多機器藏在 NAT / 防火牆後面——你裝得了 Agent，卻開不了入站埠。  
> AIOps 用 **反向連線 Agent** 把「監控 + Web 終端/桌面 + 告警」收進同一個自託管控制面：一條 `docker compose` 起服務端，目標機貼上一條安裝指令即可納管。

**目前版本 [v1.0.6](https://github.com/sreyun/aiops/releases/tag/v1.0.6)** · 鏡像：[GitHub](https://github.com/sreyun/aiops) / [Gitee](https://gitee.com/bigdatasafe/aiops) · [CHANGELOG](CHANGELOG.md)

> 安裝與進階文件以 [简体中文](INSTALL.md) / [English](INSTALL_EN.md) 為準；本頁提供繁中入口敘事。

---

## 先看這一條路徑（3 分鐘）

```bash
docker compose up -d
open http://localhost:8529
# 在「安裝指令」頁複製命令，到目標主機執行（Agent 反向連回，無需入站埠）
# curl -fsSL "http://<server>:8529/install.sh?token=<TOKEN>" | sudo sh
```

上線後立刻驗證：

1. **主機列表出現線上機器**（CPU / 記憶體 / 磁碟開始上報）  
2. **點開 Web 終端**，能在瀏覽器進入 shell  
3. **設一條閾值告警**，確認飛書 / 釘釘 / 郵件能收到  

這就是尖刀場景：**反向連線維運台**。其餘能力都建立在這條路徑上。

---

## 為什麼選 AIOps

| | |
|---|---|
| **反向連線，少改網路** | Agent 主動連回 Server；終端、桌面、轉發走同一隧道 |
| **單一二進位 + 零依賴 Agent** | 服務端一個 Go 行程；Agent 標準庫採集 |
| **資料在你自己機器上** | PostgreSQL + VictoriaMetrics，全部自架，無強制遙測 |

> 產品可以很廣——**對外入口只講清上面這一條路徑。**

---

## 能力全景（按需展開）

```
尖刀路徑（建議先用）
  主機監控 → 告警治理 → Web 終端 / 遠端桌面 → 連接埠轉發

平台擴充（同一控制面）
  撥測 / API 監控 · 日誌 · 劇本 · SRE · AI / MCP · 安全中心
  Hyper-V / 容器 / K8s · SNMP / NetFlow / Redfish · SQL · 行動端*
```

\* 行動端為獨立發佈包，原始碼不在本倉庫。

<details>
<summary><b>主機與資源監控</b></summary>

- 跨平台原生採集：CPU / 記憶體 / 磁碟 / 行程 / 連接埠 / 網路 / GPU 等  
- 帶外採集（目標可無 Agent）：Redfish、NetFlow、OceanStor、SNMP、容器 / Hyper-V / K8s  
- 全域資源搜尋與拓撲輔助  

</details>

<details>
<summary><b>告警、撥測與可觀測</b></summary>

- 閾值 + 靜音 / 抑制 / 路由；飛書、釘釘、郵件、簡訊、語音  
- 撥測：Ping / TCP / HTTP / 行程；API 可用率 / P95  
- 日誌增量採集 + 全文檢索；時序由 VictoriaMetrics 承載  

</details>

<details>
<summary><b>遠端終端、桌面與轉發</b></summary>

- Web 終端：多標籤、錄製回放、唯讀旁觀、指令稽核、二次密碼  
- Web 遠端桌面：JPEG / H.264；Windows 鎖定畫面需 **服務安裝**  
- 連接埠轉發 / HTTP 反代（含 WebSocket），SSRF 防護  

</details>

<details>
<summary><b>自動化、SRE 與 AI</b></summary>

- 劇本、自動修復護欄、事件 / SLO / 工單  
- AI 巡檢與根因研判；pgvector RAG；Hermes 對話  
- MCP：可對 Cursor / Claude 暴露唯讀工具  

</details>

---

## 安裝部署

> 服務端**強依賴** PostgreSQL 與 VictoriaMetrics。

```bash
docker compose up -d
```

造訪 `http://localhost:8529` 完成首次安全初始化，建議開啟 MFA。  
細節見 [INSTALL.md](INSTALL.md) / [INSTALL_EN.md](INSTALL_EN.md)。

---

## 建議使用路徑

1. 納管主機 → 確認上線  
2. 看監控 → 再加撥測 / API 監控  
3. 收告警 → 接 IM / 郵件  
4. 遠端排障 → Web 終端；Windows 桌面請用服務安裝  
5. 再擴充 → 劇本、SRE、AI / MCP、安全中心  

經典 UI：`/` · Vue：`/v2/`

---

## 架構一覽

```
瀏覽器 / 行動端* ──REST/WS──► Go 服務端 ──► PostgreSQL
                                  │            VictoriaMetrics
                                  ▲
                            反向連線 / 上報
                                  │
                            Go Agent（指標 + 終端/桌面隧道）
```

---

## 參與貢獻與授權

歡迎 Issue / PR。建議從尖刀路徑（Agent / 監控 / 終端）起步。  
**AGPL-3.0** — 見 [LICENSE](../LICENSE)。內部自架自用免費且不限主機數；閉源分發、整合轉售或對外提供服務需商業授權，見 [LICENSING.md](../LICENSING.md)。`v1.1.119` 及更早版本仍為 MIT 且永久有效。GitHub：<https://github.com/sreyun/aiops> · Gitee：<https://gitee.com/bigdatasafe/aiops>

---

<p align="center">
  <b>先跑通「裝上 Agent → 看見主機 → 打開終端」。</b>
</p>
