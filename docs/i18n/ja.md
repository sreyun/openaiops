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

## 📸 製品スクリーンショット

### Web コンソール

<table>
  <tr>
    <td align="center"><b>概要ダッシュボード</b><br/><br/><img src="../image/1-shouye.png" alt="概要ダッシュボード" width="100%"><br/>クラスターリソース、アラート、アクティビティの統合ビュー：ホストオンライン率、システムヘルスステータス、アクティブアラートの概要；CPU / GPU / メモリ / ディスク / IO / IOPS リソース TOP10 リアルタイムランキング、ボトルネックホストを一目で特定。</td>
    <td align="center"><b>ホスト管理</b><br/><br/><img src="../image/2-zhuji.png" alt="ホスト管理" width="100%"><br/>左側のアセットツリーはデータセンター / 業務でグループ化、右側のカードスタイル表示は各ホストのリアルタイムメトリクスを表示——CPU、メモリ、スワップ、各ディスクパーティション、1/5/15 分負荷、ネットワークスループット、IOPS、プロセスと接続数、グリッド / リストデュアルビューをサポート。</td>
  </tr>
  <tr>
    <td align="center"><b>Web ターミナル</b><br/><br/><img src="../image/3-zhongduan.png" alt="Web ターミナル" width="100%"><br/>Agent リバースチャネル経由でターゲットホストに直接接続、SSH インバウンドポートを開く必要なし。マルチタブで複数ホストに同時接続、コマンド監査と録画再生、オブザーバーモードをサポート。</td>
    <td align="center"><b>リモートデスクトップ</b><br/><br/><img src="../image/4-zhuomian.png" alt="リモートデスクトップ" width="100%"><br/>JPEG / H.264 デュアルエンコーディングリモートデスクトップ、マルチ画面切り替え、解像度自動適応、Ctrl+Alt+Del などのシステムショートカットをサポート；右側パネルはファイルアップロード/ダウンロードとクリップボード同期を提供、ローカルデスクトップに近い操作体験。</td>
  </tr>
  <tr>
    <td align="center"><b>Agent インストール</b><br/><br/><img src="../image/5-agent.png" alt="Agent インストール" width="100%"><br/>1 つのコマンドで Agent をデプロイ、Linux / Windows / macOS 3 プラットフォームをサポート。標準モード、ゲートウェイリレーモード、マルチサーバープッシュモードを選択可能；Token 戦略と自動更新戦略はコンソールで一元管理。</td>
    <td align="center"><b>ハードウェアリソース監視</b><br/><br/><img src="../image/6-jiqi.png" alt="ハードウェアリソース監視" width="100%"><br/>Redfish / BMC / iDRAC / iLO 経由で物理サーバーのハードウェアステータスを帯外収集：ベンダー、モデル、シリアル番号、電源/温度/消費電力、BIOS バージョン；BMC イベントログ（SEL）を完全に保持、AI 診断をサポート。</td>
  </tr>
  <tr>
    <td align="center"><b>コンテナ管理</b><br/><br/><img src="../image/7-docker.png" alt="コンテナ管理" width="100%"><br/>ホスト上の Docker / Podman コンテナと Compose プロジェクトを一元管理：リアルタイムステータス、ポートマッピング、イメージ情報を一目で確認；ワンクリックで開始/停止、再起動、ログ表示、クロスホストバッチフィルタリングをサポート。</td>
    <td align="center"><b>Playbook オーケストレーション</b><br/><br/><img src="../image/8-juben.png" alt="Playbook オーケストレーション" width="100%"><br/>ビジュアル自動化運用 Playbook：システム検査、ネットワーク検査、セキュリティ検査、systemd サービス再起動、K8s Deployment ローリング再起動、ディープホスト検査、Java アプリケーション検査/パフォーマンス分析/例外分析などの組み込み Playbook がすぐに使用可能、カスタムマルチステップ並列と承認ガードレールをサポート。</td>
  </tr>
  <tr>
    <td align="center"><b>SRE ハブ</b><br/><br/><img src="../image/9-sre.png" alt="SRE ハブ" width="100%"><br/>アラートトリガー / SLO バーンダウン / 手動作成されたイベントがここに集約、完全なタイムラインと自動修復記録を含む。8 つのサブモジュールをサポート：インシデント、自動修復、依存トポロジー、SLO、チケット、On-call、変更、プラットフォームヘルス検査。</td>
    <td align="center"><b>AI 診断</b><br/><br/><img src="../image/10-ai.png" alt="AI 診断" width="100%"><br/>SRE イベントリストでワンクリック AI アシスタントを起動、現在のアラートの根本原因を自動分析し、処理提案を提供。AI はアラートの相関関係を整理し、類似ケースを検索し、重要なホストのヘルスステータスを確認し、思考プロセスは完全に可視。</td>
  </tr>
  <tr>
    <td align="center"><b>アラート設定</b><br/><br/><img src="../image/11-setting.png" alt="アラート設定" width="100%"><br/>マルチチャネルアラートプッシュ設定：Feishu、DingTalk、Webhook、メール、SMS、電話 6 チャネルから選択可能；サイレンス / 抑制 / ルーティング戦略をサポート、重要は電話 SMS、警告は IM、アラートストームを回避。</td>
    <td align="center"><b>AI 設定</b><br/><br/><img src="../image/12-aiset.png" alt="AI 設定" width="100%"><br/>ワンストップ AI 機能設定：対話モデル（OpenAI 互換 / 百煉 / DeepSeek / Ollama / Anthropic / Claude）、RAG ベクトルライブラリ、判断とコスト（MoA / 単価）、MCP 統合、呼び出し観測、セキュリティ認証 6 項目設定、音声入力/放送をサポート。</td>
  </tr>
</table>

### モバイル App（Android / HarmonyOS）

> **注意**：モバイル App（Android / HarmonyOS）は独立配布パッケージであり、**オープンソースコミュニティバージョンでは App インストールパッケージを提供していません**。モバイル端を使用する必要がある場合は、プロジェクトチームにお問い合わせください。

<table>
  <tr>
    <td align="center"><b>SRE コックピット</b><br/><br/><img src="../image/app01.jpg" alt="SRE コックピット" width="100%"><br/>モバイル概要ページ：ホストオンライン率、重大/警告アラート数が一目で確認；クイックエントリーはハードウェア監視、仮想マシン、ネットワークトラフィック、ダイヤルテスト、ホスト監視、ログ検索、運用オーケストレーション、ダッシュボードをカバー；保留中のインシデントは優先度別にソート。</td>
    <td align="center"><b>インフラ監視</b><br/><br/><img src="../image/app02.jpg" alt="インフラ監視" width="100%"><br/>モバイルインフラページ：ホスト/リソース/ネットワーク/ダイヤルテストの 4 次元切り替え；GPU リソース概要（モデル、VRAM、温度）；ホストリストはグループ別にフィルタリング、CPU、メモリ、ディスクなどのコアメトリクスをリアルタイム表示。</td>
  </tr>
  <tr>
    <td align="center"><b>モバイルターミナル</b><br/><br/><img src="../image/app03.jpg" alt="モバイルターミナル" width="100%"><br/>モバイル Web ターミナル：Agent リバースチャネル経由でターゲットホストに直接接続、完全なターミナルインタラクティブ体験；ショートカットキー、フォントスケーリング、画面回転をサポート、いつでもどこでも問題排查。</td>
    <td align="center"><b>AI 運用アシスタント</b><br/><br/><img src="../image/app04.jpg" alt="AI 運用アシスタント" width="100%"><br/>モバイル AI 対話：自然言語で問題を説明、AI が自動的に歴史案例を検索し、アラート詳細を取得し、ホストヘルスステータスを確認し、根本原因分析と処理提案を提供；ボトムナビゲーションバーは概要/監視/アラート/運用/AI 5 つの主要エントリーをカバー。</td>
  </tr>
</table>

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
