<div align="center">

# AIOps

**インバウンドポート不要のセルフホスト運用コンソール：ホストを見る · ターミナル/デスクトップを開く · アラートを収める**

[![Version](https://img.shields.io/badge/Version-v1.0.6-blue)](https://github.com/sreyun/aiops/releases/tag/v1.0.6)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](../LICENSE)
[![Platforms](https://img.shields.io/badge/Platforms-Linux%20%7C%20Windows%20%7C%20macOS-lightgrey)]()

**[简体中文](README.md) · [繁體中文](README.zh-TW.md) · [English](README_EN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Deutsch](README.de.md) · [Français](README.fr.md) · [Español](README.es.md) · [Português](README.pt-BR.md) · [Русский](README.ru.md)**

</div>

> 多くのマシンは NAT / ファイアウォールの内側にあり、Agent は入れてもインバウンドは開けません。  
> AIOps は **逆接続 Agent** で「監視 + Web ターミナル/デスクトップ + アラート」を一つのセルフホスト制御面にまとめます。サーバーは `docker compose`、対象ホストはインストールコマンドを貼るだけです。

**現行リリース [v1.0.6](https://github.com/sreyun/aiops/releases/tag/v1.0.6)** · [GitHub](https://github.com/sreyun/aiops) / [Gitee](https://gitee.com/bigdatasafe/aiops) · [CHANGELOG](CHANGELOG.md)

---

## まずこの一本（約 3 分）

```bash
docker compose up -d
open http://localhost:8529
# UI の「インストールコマンド」を対象ホストで実行（Agent が外向き接続、インバウンド不要）
# curl -fsSL "http://<server>:8529/install.sh?token=<TOKEN>" | sudo sh
```

直後にこの 3 点を確認してください：

1. **ホスト一覧にオンライン機が出る**（CPU / メモリ / ディスクが上がる）  
2. **Web ターミナルが開ける**  
3. **閾値アラート 1 本**で Feishu / DingTalk / メールが届く  

これが尖刀シーン：**逆接続の運用コンソール**です。他機能はこの上に載ります。

---

## なぜ AIOps か

| | |
|---|---|
| **逆接続でネットワーク変更が少ない** | Agent が Server へ外向き接続。ターミナル / デスクトップ / 転送も同じトンネル |
| **単一バイナリ + 依存ゼロ Agent** | サーバーは Go 1 本。Agent は標準ライブラリ収集（Linux / Windows / macOS / Kylin） |
| **データは自前** | PostgreSQL + VictoriaMetrics、完全セルフホスト、強制テレメトリなし |

> 「死活監視だけ」「ダッシュボードだけ」の小ツールではありません。中小チームがよく継ぎ接ぎする監視 + アラート + 遠隔切り分けを一つのプラットフォームに収束します。  
> 機能は広くてよい——**入口の説明は狭く保つ。**

---

## 目次

- [まずこの一本](#まずこの一本約-3-分)
- [なぜ AIOps か](#なぜ-aiops-か)
- [能力マップ](#能力マップ)
- [最近のハイライト](#最近のハイライト)
- [インストール](#インストール)
- [推奨利用パス](#推奨利用パス)
- [設定の要点](#設定の要点)
- [アーキテクチャ](#アーキテクチャ)
- [ドキュメントと境界](#ドキュメントと境界)
- [コントリビューション](#コントリビューション)
- [ライセンス](#ライセンス)

---

## 能力マップ

主経路を先に通す。詳細は必要になったら開く。

```
尖刀パス（まずここ）
  ホスト監視 → アラート治理 → Web ターミナル / リモートデスクトップ → ポート転送

プラットフォーム拡張（同じ制御面）
  プローブ / API 監視 · ログ · Playbook · SRE（インシデント / SLO / チケット）
  AI 巡検 / Hermes / MCP · セキュリティセンター · Hyper-V / コンテナ / K8s
  SNMP / NetFlow / Redfish · SQL ツール · Android / HarmonyOS コンソール*
```

\* モバイルは別配布。ソースは本リポジトリに含まれません。

<details>
<summary><b>ホストとリソース監視</b></summary>

- ネイティブ収集：CPU / メモリ / ディスク / プロセス / ポート / ネットワーク / DiskIO / IOPS / GPU / 負荷  
- 帯域外（対象に Agent 不要）：Redfish、NetFlow、Huawei OceanStor、SNMP、コンテナ / Hyper-V / K8s  
- グローバル検索とトポロジ補助  

</details>

<details>
<summary><b>アラート・プローブ・可観測性</b></summary>

- 閾値 + サイレンス / 抑制 / ルート；Feishu、DingTalk、メール、SMS、音声  
- プローブ：Ping / TCP / HTTP / プロセス；API 可用率 / P95 / スループット  
- ログ収集（暗号化アップロード可）+ 全文検索；時系列は VictoriaMetrics  

</details>

<details>
<summary><b>ターミナル・デスクトップ・転送</b></summary>

- Web ターミナル：タブ、録画再生、閲覧専用、コマンド監査、二次パスワード  
- Web リモートデスクトップ：JPEG / H.264；Windows ロック画面は **サービスインストール** が必要  
- ポート転送 / HTTP リバースプロキシ（WebSocket 含む）、SSRF 出口防護  

</details>

<details>
<summary><b>自動化・SRE・AI</b></summary>

- Playbook、承認付き自動修復、インシデント / SLO / チケット、統合受信箱  
- AI 巡検と RCA（OpenAI 互換）；pgvector RAG；Hermes 対話  
- MCP：Cursor / Claude へ読み取り専用ツール公開、外部 MCP も接続可  

</details>

<details>
<summary><b>セキュリティとモバイル</b></summary>

- RBAC、任意 MFA、監査、マシン指紋、静的暗号化、任意 TLS、セキュリティセンター  
- Android / HarmonyOS は別配布；プッシュは自前の長接続  

</details>

---

## 最近のハイライト

| 領域 | 内容 |
|---|---|
| **デュアル UI** | クラシック + Vue コンソール（`/v2/`） |
| **MCP** | Streamable HTTP の当直 / 診断ツール；外部 MCP Clients |
| **Agent 遠隔更新** | 一括配信；Windows はバージョン ACK 後に成功 |
| **デスクトップ強化** | Windows ロック / CAD / 解除の継続改善（サービスインストール） |

詳細：[CHANGELOG.md](CHANGELOG.md) · [Releases](https://github.com/sreyun/aiops/releases)

---

## インストール

> サーバーは **PostgreSQL と VictoriaMetrics の両方が必須**です。

```bash
docker compose up -d
# 開発: docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build
```

`http://localhost:8529` で初回セキュリティ初期化（強制パスワード変更）。その後 MFA 推奨。

任意の強化ブートストラップ：[`scripts/secure-compose.sh`](scripts/secure-compose.sh)

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/sreyun/aiops/master/scripts/secure-compose.sh)
docker compose up -d
```

バイナリ / ソース：

```bash
export AIOPS_POSTGRES_DSN="postgres://aiops:secret@localhost:5432/aiops?sslmode=disable"
export AIOPS_VM_URL="http://localhost:8428"
./aiops-server
cp config.example.yaml config.yaml && ./aiops-agent --config config.yaml
# Go 1.26+: go build ./cmd/server ./cmd/agent
```

詳細：[INSTALL.md](INSTALL.md) · [DEPLOY_GUIDE.md](DEPLOY_GUIDE.md) · 英語版 [INSTALL_EN.md](INSTALL_EN.md)

---

## 推奨利用パス

1. **ホスト登録** → インストールコマンド → オンライン確認  
2. **監視を見る** → ホスト詳細；必要ならプローブ / API 監視  
3. **アラートを受ける** → 閾値と治理 → IM / メール  
4. **遠隔切り分け** → Web ターミナル；Windows デスクトップはサービスインストール  
5. **拡張** → Playbook、SRE、AI / MCP、セキュリティ、SNMP …

クラシック UI：`/` · Vue：`/v2/` または `/?ui=v2`

---

## 設定の要点

| 変数 | 用途 | 必須 |
|---|---|---|
| `AIOPS_POSTGRES_DSN` | PostgreSQL | はい |
| `AIOPS_VM_URL` | VictoriaMetrics | はい |
| `AIOPS_LISTEN` | 待受（既定 `:8529`） | いいえ |
| `AIOPS_SECRET_KEY` | 設定の AES-GCM 暗号化 | 本番推奨 |
| `AIOPS_TLS_CERT` / `AIOPS_TLS_KEY` | HTTPS | 本番推奨 |

Agent は `server` / `token` / `category`、レポート間隔、`servers[]`、ログ、中継、Redfish / SNMP など。  
例：[config.example.yaml](config.example.yaml)

---

## アーキテクチャ

```
Browser / mobile* ──REST/WS──► Go server ──► PostgreSQL
                                  │           VictoriaMetrics
                                  ▲
                     逆接続 / レポート
                                  │
                            Go agent（メトリクス + ターミナル/デスクトップ隧道）
```

両方のストア欠落時は起動拒否。同じ Agent 面で帯域外収集やプラグインへ拡張可能。

---

## ドキュメントと境界

| ドキュメント | 内容 |
|---|---|
| [USER_GUIDE.md](USER_GUIDE.md) | 利用シーン |
| [INSTALL.md](INSTALL.md) / [DEPLOY_GUIDE.md](DEPLOY_GUIDE.md) | 導入 |
| [FORWARD_GUIDE.md](FORWARD_GUIDE.md) | ポート転送 |
| [docs/year1-acceptance.md](docs/year1-acceptance.md) | POC 受入 |

**境界**：単一インスタンスに規模上限あり；LLM 未設定時はヒューリスティック；Windows ロック画面デスクトップはサービスインストール必須；モバイルは別配布。

---

## コントリビューション

Issue / PR / ドキュメント歓迎。モジュールが多いので、**尖刀パス関連から**始めるとレビューしやすいです：

1. Agent インストール / レポート / 逆チャネル  
2. ホスト監視とアラート UX  
3. Web ターミナル / デスクトップと導入ドキュメント  

開発：Go 1.26+；`make build` · `make audit`。セキュリティ問題は公開 Issue にせず私信へ。

| | |
|---|---|
| GitHub | <https://github.com/sreyun/aiops> |
| Gitee | <https://gitee.com/bigdatasafe/aiops> |
| Releases | <https://github.com/sreyun/aiops/releases> |

---

## ライセンス

**AGPL-3.0** — [LICENSE](../LICENSE)。社内での自己ホスト利用は無償・ホスト数無制限。
クローズドソースでの配布、自社製品への組み込み、改変版のネットワークサービス提供（AGPL 第13条）には**商用ライセンス**が必要です：[LICENSING.md](../LICENSING.md)。

> `v1.1.119` 以前のバージョンは MIT のまま有効です（遡及しません）。  

`vendor/` は各ライセンスに従います。モバイルクライアントは別配布です。

---

<p align="center">
  <b>まず「Agent を入れる → ホストを見る → ターミナルを開く」。<br/>他の能力は、あなたが完全に掌控する同じプラットフォームの中にあります。</b>
</p>
