<div align="center">

# AIOps

**オープンソースのセルフホスト型ホスト監視 & SRE プラットフォーム**  
観測 · アラート · 自動修復 · リモート運用 · Agent OTA · AI 診断 — 完全に自分で制御できる 1 バイナリへ。

[![Version](https://img.shields.io/badge/Version-v0.20.49-blue)](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL%203.0-blue.svg)](../../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Android%20%7C%20HarmonyOS-lightgrey)]()
[![Stars](https://img.shields.io/github/stars/sreyun/openaiops?style=social)](https://github.com/sreyun/openaiops)

**[简体中文](../../README.md) · [繁體中文](zh-TW.md) · [English](en.md) · [日本語](ja.md) · [한국어](ko.md) · [Français](fr.md) · [Deutsch](de.md) · [Español](es.md) · [Português](pt-BR.md) · [Русский](ru.md)**

[クイックスタート](#-クイックスタート) · [コア機能](#-コア機能) · [ドキュメント](../README.md) · [変更履歴](../../CHANGELOG.md) · [Releases](https://github.com/sreyun/openaiops/releases)

</div>

---

## なぜ AIOps か

監視・アラート・Bastion・Runbook が別々に増え、商用製品はホスト課金でデータはクラウド側に残りがちです。

AIOps はよく使う経路を **1 つのセルフホスト基盤** にまとめます：

| | AIOps | 典型的な寄せ集め |
|---|---|---|
| **構成** | Go サーバー 1 + 依存ゼロ Agent 1 | Zabbix / Prometheus / Grafana / Alertmanager / Bastion / Runbook… |
| **導入** | `docker compose up -d`（約 3 分） | 連携に数日 |
| **データ** | PostgreSQL + VictoriaMetrics（**自社保持**） | SaaS や分散 DB |
| **リモート** | Web 端末／デスクトップ／ポート転送、Agent **外向きのみ** | 別途 VPN／Bastion |
| **フリート** | **Agent OTA 自動更新**（SHA-256 検証、メンテナンスウィンドウ、一括 push、ロールバック） | ホストごとに SSH で差し替え |
| **ループ** | アラート → Playbook → インシデント／SLO／チケット → AI RCA | 人手でつなぐ |
| **ライセンス** | **AGPL-3.0**、ホスト数制限なし | ノード／モジュール課金 |

> プライベート DC・ハイブリッドクラウド、可視化・制御・変更安全・説明可能な運用を求めるチーム向け。

---

## ✨ コア機能

機能の羅列ではなく、7 本の柱：

```
  Observe ──────► Govern ──────► Remediate ──────► Diagnose
  Hosts/GPU/logs   Silence/route   Playbooks/gates   AI · RAG · MCP
  Probes/OOB       Multi-channel   Incident/SLO      Evidence gate

  Remote · terminal/desktop/forward (reverse tunnel)   Fleet · Agent OTA
  Security · RBAC/MFA/FIM
```

1. **観測** — クロスプラットフォーム Agent（Linux／Windows／macOS／Kylin）、GPU、ログ、HTTP／TCP プローブ、API SLI、Redfish／SNMP／NetFlow／コンテナ／K8s／Hyper-V。
2. **ガバナンス** — 閾値プリセット、silence／inhibit／route；Feishu／DingTalk／メール／SMS／音声。
3. **修復 & SRE** — 承認ガード付き Playbook；インシデント、SLO、チケット、凍結ウィンドウ、監査付き break-glass。
4. **AI 診断** — 点検＋RCA（OpenAI 互換、未設定時はヒューリスティック）；pgvector RAG、Skills、MCP（Cursor／Claude）；音声セルフテスト。
5. **リモート運用** — Web 端末（再生／観戦／監査／二次パスワード）、リモートデスクトップ（JPEG／H.264）、ポート転送／HTTP プロキシと SSRF 防御。
6. **セキュアな提供** — RBAC、MFA、Agent 指紋、AES-256-GCM；Android／HarmonyOS は別配布。
7. **Agent OTA** — サーバー更新後、遅れているオンライン Agent を自動キュー（デフォルト ON）；コンソール一括 push または `POST /api/v1/agents/update`；`/dl/` から SHA-256 検証付きダウンロード、`.bak` ロールバック。

現行リリース **[v0.20.49](https://github.com/sreyun/openaiops/releases/tag/v0.20.49)** · [GitHub](https://github.com/sreyun/openaiops)／[Gitee](https://gitee.com/bigdatasafe/openaiops)

---

## 🚀 クイックスタート

> サーバーは PostgreSQL と VictoriaMetrics の**両方が必須**です。

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

詳細インストール → **[../getting-started/install.en.md](../getting-started/install.en.md)** · 本番 → **[../getting-started/deploy.en.md](../getting-started/deploy.en.md)**

---

## 🏗 アーキテクチャ

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

## 📚 ドキュメント

長文と多言語 README は [`docs/`](../README.md) に集約。ルートには中国語 README と CHANGELOG のみ残します。

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

## 🤝 貢献

Issue／PR／翻訳を歓迎します。目安：`make build` · `make audit`。

AIOps が寄せ集めスタックを置き換えたら、**ぜひ Star** をお願いします。

---

## ライセンス

[AGPL-3.0](../../LICENSE)。ホスト数制限なし。モバイルは別パッケージ（本リポジトリにソースなし）。

---

<p align="center">
  <b>AIOps · 運用の複雑さを、自分で所有する基盤へ。</b><br/>
  <sub>Star ⭐ · Fork · Issue · セルフホスト運用を一緒に</sub>
</p>
