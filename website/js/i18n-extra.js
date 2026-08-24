/* ============================================================
   i18n-extra.js — AIOps 扩展字典与渲染
   由 i18n.js 在 T 定义后，通过 window.__I18N_EXTRA 深度合并。
   内容：
     1) _common 扩展：新增 nav.pricing / nav.cases（四语）+ 完整日本語 _common
     2) features.ja / solutions.ja 完整镜像（与 zh-CN 结构一致）
     3) pricing / cases 四语（zh-CN / zh-TW / en / ja）字典
     4) window.__renderPricing / window.__renderCases 渲染函数
   说明：本文件独立于 i18n.js，外部格式化器不会改写，规避异步回写风险。
   ============================================================ */
(function () {
  "use strict";

  window.__I18N_EXTRA = {
  "_common": {
    "zh-CN": {
      "nav.pricing": "定价方案", "nav.pricingNew": "定价与支持", "nav.ctaNew": "开始使用", "nav.casesGroup": "客户案例",
      "nav.cases": "客户案例", "nav.product": "产品",
      "hero.stat1.unit": " 分钟", "hero.stat3.unit": " 平台", "hero.stat4.unit": "%",
      "footer.trust": "信任", "footer.community": "社区", "footer.legal": "法律", "footer.changelog": "更新日志",
      "learnMore": "了解更多 →",
      "cta.trust1": "无需信用卡", "cta.trust2": "无需注册", "cta.trust3": "AGPL-3.0 开源", "cta.trust4": "数据完全自持",
      "nextPages.title": "接下来你可能想了解",
      "nextPages.featuresDesc": "看看这些功能如何解决你的运维场景",
      "nextPages.features2sol": "按场景查看落地方案", "nextPages.features2pricing": "开源免费，按需选择支持", "nextPages.features2compare": "与 Zabbix / Prometheus 全面对比",
      "nextPages.solutionsDesc": "看看案例与对比，验证落地效果",
      "nextPages.solutions2cases": "按行业查看落地范式", "nextPages.solutions2compare": "与 Zabbix / Prometheus 全面对比", "nextPages.solutions2pricing": "开源免费，按需选择支持",
      "nextPages.compareDesc": "看看定价与功能详情",
      "nextPages.compare2pricing": "开源免费，按需选择支持", "nextPages.compare2features": "深入了解每个功能", "nextPages.compare2cases": "按行业查看落地范式",
      "nextPages.pricingDesc": "深入了解功能和联系方式",
      "nextPages.pricing2contact": "获取企业级支持方案", "nextPages.pricing2features": "深入了解每个功能", "nextPages.pricing2cases": "按行业查看落地范式",
      "nextPages.casesDesc": "看看方案与联系方式",
      "nextPages.cases2sol": "按场景查看落地方案", "nextPages.cases2contact": "有疑问？直接联系我们", "nextPages.cases2pricing": "开源免费，按需选择支持",
      "nextPages.faqDesc": "看看案例与方案，找到适合你的场景",
      "nextPages.faq2cases": "按行业查看落地范式", "nextPages.faq2sol": "按场景查看落地方案", "nextPages.faq2contact": "有疑问？直接联系我们",
      "nextPages.contactDesc": "了解更多产品与方案信息",
      "nextPages.contact2pricing": "开源免费，按需选择支持", "nextPages.contact2features": "深入了解每个功能", "nextPages.contact2cases": "按行业查看落地范式"
    },
    "zh-TW": {
      "nav.pricing": "定價方案", "nav.pricingNew": "定價與支持", "nav.ctaNew": "開始使用", "nav.casesGroup": "客戶案例",
      "nav.cases": "客戶案例", "nav.product": "產品",
      "hero.stat1.unit": " 分鐘", "hero.stat3.unit": " 平台", "hero.stat4.unit": "%",
      "footer.trust": "信任", "footer.community": "社區", "footer.legal": "法律", "footer.changelog": "更新日誌",
      "learnMore": "了解更多 →",
      "cta.trust1": "無需信用卡", "cta.trust2": "無需註冊", "cta.trust3": "AGPL-3.0 開源", "cta.trust4": "資料完全自持",
      "nextPages.title": "接下來你可能想了解",
      "nextPages.featuresDesc": "看看這些功能如何解決你的運維場景",
      "nextPages.features2sol": "按場景查看落地方案", "nextPages.features2pricing": "開源免費，按需選擇支持", "nextPages.features2compare": "與 Zabbix / Prometheus 全面對比",
      "nextPages.solutionsDesc": "看看案例與對比，驗證落地效果",
      "nextPages.solutions2cases": "按行業查看落地範式", "nextPages.solutions2compare": "與 Zabbix / Prometheus 全面對比", "nextPages.solutions2pricing": "開源免費，按需選擇支持",
      "nextPages.compareDesc": "看看定價與功能詳情",
      "nextPages.compare2pricing": "開源免費，按需選擇支持", "nextPages.compare2features": "深入了解每個功能", "nextPages.compare2cases": "按行業查看落地範式",
      "nextPages.pricingDesc": "深入了解功能和聯繫方式",
      "nextPages.pricing2contact": "獲取企業級支持方案", "nextPages.pricing2features": "深入了解每個功能", "nextPages.pricing2cases": "按行業查看落地範式",
      "nextPages.casesDesc": "看看方案與聯繫方式",
      "nextPages.cases2sol": "按場景查看落地方案", "nextPages.cases2contact": "有疑問？直接聯繫我們", "nextPages.cases2pricing": "開源免費，按需選擇支持",
      "nextPages.faqDesc": "看看案例與方案，找到適合你的場景",
      "nextPages.faq2cases": "按行業查看落地範式", "nextPages.faq2sol": "按場景查看落地方案", "nextPages.faq2contact": "有疑問？直接聯繫我們",
      "nextPages.contactDesc": "了解更多產品與方案信息",
      "nextPages.contact2pricing": "開源免費，按需選擇支持", "nextPages.contact2features": "深入了解每個功能", "nextPages.contact2cases": "按行業查看落地範式"
    },
    "en": {
      "nav.pricing": "Pricing", "nav.pricingNew": "Pricing & Support", "nav.ctaNew": "Get Started", "nav.casesGroup": "Customers",
      "nav.cases": "Customers", "nav.product": "Product",
      "hero.stat1.unit": " min", "hero.stat3.unit": " platforms", "hero.stat4.unit": "%",
      "footer.trust": "Trust", "footer.community": "Community", "footer.legal": "Legal", "footer.changelog": "Changelog",
      "learnMore": "Learn more →",
      "cta.trust1": "No credit card", "cta.trust2": "No registration", "cta.trust3": "AGPL-3.0 Open Source", "cta.trust4": "Fully self-hosted",
      "nextPages.title": "You might also want to explore",
      "nextPages.featuresDesc": "See how these features solve your ops scenarios",
      "nextPages.features2sol": "Browse solutions by scenario", "nextPages.features2pricing": "Open source, pick your support tier", "nextPages.features2compare": "Full comparison with Zabbix / Prometheus",
      "nextPages.solutionsDesc": "Explore cases and comparisons to validate results",
      "nextPages.solutions2cases": "Browse use cases by industry", "nextPages.solutions2compare": "Full comparison with Zabbix / Prometheus", "nextPages.solutions2pricing": "Open source, pick your support tier",
      "nextPages.compareDesc": "Explore pricing and feature details",
      "nextPages.compare2pricing": "Open source, pick your support tier", "nextPages.compare2features": "Dive into every feature", "nextPages.compare2cases": "Browse use cases by industry",
      "nextPages.pricingDesc": "Explore features and get in touch",
      "nextPages.pricing2contact": "Get enterprise support", "nextPages.pricing2features": "Dive into every feature", "nextPages.pricing2cases": "Browse use cases by industry",
      "nextPages.casesDesc": "Explore solutions and get in touch",
      "nextPages.cases2sol": "Browse solutions by scenario", "nextPages.cases2contact": "Questions? Reach out to us", "nextPages.cases2pricing": "Open source, pick your support tier",
      "nextPages.faqDesc": "Explore cases and solutions for your scenario",
      "nextPages.faq2cases": "Browse use cases by industry", "nextPages.faq2sol": "Browse solutions by scenario", "nextPages.faq2contact": "Questions? Reach out to us",
      "nextPages.contactDesc": "Learn more about the product and solutions",
      "nextPages.contact2pricing": "Open source, pick your support tier", "nextPages.contact2features": "Dive into every feature", "nextPages.contact2cases": "Browse use cases by industry"
    },
    "ja": {
      "nav.home": "ホーム",
      "nav.features": "機能", "nav.solutions": "ソリューション", "nav.comparison": "製品比較",
      "nav.faq": "よくある質問", "nav.contact": "お問い合わせ",
      "nav.pricing": "料金プラン", "nav.pricingNew": "料金とサポート", "nav.ctaNew": "はじめる", "nav.casesGroup": "導入事例",
      "nav.cases": "導入事例", "nav.product": "製品",
      "nav.cta": "無料で導入", "nav.deploy": "今すぐ導入 →", "nav.seePain": "課題を見る",
      "footer.desc": "エンタープライズ向けホスト監視・SRE運用プラットフォーム。オープンソースで無料、PostgreSQL＋VictoriaMetrics の統合ストレージ、コマンド一つで導入。",
      "footer.product": "製品", "footer.resources": "リソース", "footer.docs": "ドキュメント", "footer.install": "導入ガイド",
      "footer.github": "GitHub", "footer.copy": "© 2026 AIOps · AGPL-3.0 License · Built with Go",
      "footer.trust": "信頼", "footer.community": "コミュニティ", "footer.legal": "法的情報", "footer.changelog": "更新履歴",
      "hero.stat1.unit": " 分", "hero.stat3.unit": " プラットフォーム", "hero.stat4.unit": "%",
      "learnMore": "詳しく見る →",
      "cta.trust1": "クレジットカード不要", "cta.trust2": "登録不要", "cta.trust3": "AGPL-3.0 オープンソース", "cta.trust4": "データ完全自持",
      "nextPages.title": "次にご確認ください",
      "nextPages.featuresDesc": "これらの機能が運用課題をどう解決するかご覧ください",
      "nextPages.features2sol": "シーン別ソリューション", "nextPages.features2pricing": "オープンソース、サポート段階を選択", "nextPages.features2compare": "Zabbix / Prometheus と徹底比較",
      "nextPages.solutionsDesc": "事例と比較で導入効果を確認",
      "nextPages.solutions2cases": "業界別導入事例", "nextPages.solutions2compare": "Zabbix / Prometheus と徹底比較", "nextPages.solutions2pricing": "オープンソース、サポート段階を選択",
      "nextPages.compareDesc": "料金と機能詳細を確認",
      "nextPages.compare2pricing": "オープンソース、サポート段階を選択", "nextPages.compare2features": "全機能详细介绍", "nextPages.compare2cases": "業界別導入事例",
      "nextPages.pricingDesc": "機能詳細とお問い合わせ",
      "nextPages.pricing2contact": "エンタープライズサポート", "nextPages.pricing2features": "全機能详细介绍", "nextPages.pricing2cases": "業界別導入事例",
      "nextPages.casesDesc": "ソリューションとお問い合わせ",
      "nextPages.cases2sol": "シーン別ソリューション", "nextPages.cases2contact": "ご質問はお気軽に", "nextPages.cases2pricing": "オープンソース、サポート段階を選択",
      "nextPages.faqDesc": "事例とソリューションで解決策を見つける",
      "nextPages.faq2cases": "業界別導入事例", "nextPages.faq2sol": "シーン別ソリューション", "nextPages.faq2contact": "ご質問はお気軽に",
      "nextPages.contactDesc": "製品とソリューションの詳細情報",
      "nextPages.contact2pricing": "オープンソース、サポート段階を選択", "nextPages.contact2features": "全機能详细介绍", "nextPages.contact2cases": "業界別導入事例"
    }
  },
  "features": {
    "ja": {
      "page.title": "機能詳細 — AIOps",
      "page.desc": "Zabbix ＋ Prometheus ＋ Grafana を組み合わせる必要はもうありません。AIOps は監視・アラート・リモートターミナル・自動化自己修復・AI診断・SREクローズドループを、真の運用課題に沿って一つのプラットフォームにまとめました。どの能力が必要か、すぐ見つかります。",
      "page.oglocale": "ja_JP",
      "head.tag": "機能詳細",
      "head.title": "必要な機能を、課題解決順に並べました",
      "head.desc": "単なる機能リストではなく、運用の真の課題に応える能力マトリックス",
      "band.tag": "真の運用課題のために",
      "band.title": "これらの機能は、毎日頭を悩ませていることへの答えです",
      "pains": [
        {
          "icon": "M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9",
          "t": "アラートストーム",
          "d": "毎日数百件のアラート、本当に重大なものが埋もれる"
        },
        {
          "icon": "M4 17l6-6-6-6M12 19h8",
          "t": "現場に行けない",
          "d": "機房分離・ポート未開放で、トラブル時に手が出ない"
        },
        {
          "icon": "M21 21l-5.2-5.2M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0z",
          "t": "根因が見つからない",
          "d": "メトリクスが赤くても、なぜ赤いか分からない"
        },
        {
          "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4",
          "t": "本番に乗せられない",
          "d": "プライベート導入のセキュリティ・コンプライアンス・信頼性を誰が保証するか"
        }
      ],
      "groups": [
        {
          "tag": "01",
          "title": "フルスタック可観測性",
          "desc": "OSからビジネスAPIまでのエンドツーエンドの可視性",
          "pain": "システムの健康状態を確認するたびに、ホストごとにSSHでtop/free/dfを叩くのか？",
          "roles": [
            "運用",
            "SRE",
            "開発者"
          ],
          "items": [
            {
              "title": "リアルタイムメトリクス監視",
              "color": "accent",
              "icon": "M22 12h-4l-3 9L9 3l-3 9H2",
              "desc1": "CPU / メモリ / SWAP / 複数ディスク / ネットワーク送受信 / システム負荷 / プロセス数 / TCP接続数 / 稼働時間 —— 5秒間隔で収集、網羅的にカバー。",
              "desc2": "メトリクスは永久保存、再起動後も継続収集で欠落なし。",
              "val": "ホストごとのSSH top/free/dfから解放"
            },
            {
              "title": "GPU監視",
              "color": "accent",
              "icon": "M9 3v2M15 3v2M9 19v2M15 19v2M3 9h2M3 15h2M19 9h2M19 15h2M9 9h6v6H9z",
              "desc1": "NVIDIA（nvidia-smi）/ AMD（Linux sysfs）/ Apple（macOS ioreg）の3プラットフォームからGPUを収集、ベストエフォート＋キャッシュ。",
              "val": "学習／レンダリング時のGPU負荷が一目でわかる"
            },
            {
              "title": "カスタムプローブ（死活監視）",
              "color": "accent",
              "icon": "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8z",
              "desc1": "HTTPステータスコード、TCPポート、Ping遅延、重要プロセスの生存 —— 4種のプローブですべての可用性シナリオをカバー。",
              "desc2": "履歴グラフ内蔵、範囲選択ズームと全画面プレビューに対応。",
              "val": "サービス到達不能をいち早く検知"
            },
            {
              "title": "インタラクティブな時系列グラフ",
              "color": "accent",
              "icon": "M3 3v18h18M7 14l4-4 3 3 5-6",
              "desc1": "Canvas自描画のグラフ。ホバー値表示、範囲選択ズーム、全画面プレビューに対応し、ダーク／ライトテーマに自動追従。",
              "val": "クリックするだけでドリルダウン、Grafanaへの切り替え不要"
            },
            {
              "title": "ホストのグループ化と概要",
              "color": "accent",
              "icon": "M3 3h7v7H3zM14 3h7v7h-7zM14 14h7v7h-7zM3 14h7v7H3z",
              "desc1": "業務／DCごとにカスタムグループを作成。概要KPIカードでオンライン／オフライン／重大アラート／警告の件数をリアルタイム表示。",
              "val": "数百台の健康状態を1画面で把握"
            },
            {
              "title": "ビジネスAPIプローブ",
              "color": "accent",
              "icon": "M12 2a10 10 0 1 0 10 10A10 10 0 0 0 12 2zM2 12h20M12 2a15 15 0 0 1 0 20 15 15 0 0 1 0-20z",
              "desc1": "ビジネスAPI（HTTP / gRPC / カスタム）にプローブを実行し、ステータスコード・遅延・応答内容が期待通りかを検証。",
              "val": "サービス障害を他社より早く検知"
            },
            {
              "title": "多軸アサーション",
              "color": "accent",
              "icon": "M9 12l2 2 4-4",
              "desc1": "ステータスコード、応答時間、キーワード／JSONフィールドのアサーションに対応し、プロトコル正当性とビジネス意味の両面を検証。",
              "val": "到達できるだけでなく、正しく応答するかも確認"
            }
          ]
        },
        {
          "tag": "02",
          "title": "アラート運用",
          "desc": "ノイズを抑え、本当に重大な障害を浮き上がらせる",
          "pain": "アラートストームの中で、本当の障害が埋もれ、当番が鈍感になっていないか？",
          "roles": [
            "運用",
            "SRE",
            "当番"
          ],
          "items": [
            {
              "title": "マルチクラウド・マルチチャネルアラート",
              "color": "warn",
              "icon": "M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9",
              "desc1": "CPU / メモリ / ディスク / 負荷 / GPU / オフラインなどのしきい値アラート。Feishu Webhook、DingTalk Webhook（HMAC署名）、SMTPメールに加え、Alibaba Cloud / Huawei Cloud / Tencent Cloud の3クラウドSMSおよび音声通話（TTS音声通知）へマルチチャネル配信。",
              "desc2": "クラウド事業者の切り替えはプロバイダ設定を一か所変えるだけで済み、デプロイ変更不要。番号には自動で+86を付与し、テンプレートパラメータはカスタマイズ可能。",
              "val": "重大アラートは電話で当番を叩き起こす"
            },
            {
              "title": "27項目のしきい値カスタマイズ",
              "color": "warn",
              "icon": "M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6",
              "desc1": "warn / crit の27グループのきめ細かなしきい値。ホストリソース、プローブ、APIビジネス、オーケストレーションタスク、ポート転送の5次元をカバー。項目ごとに調整可能、保存で即反映。",
              "desc2": "ホスト次元には保守／標準／緩和の3プリセットを内蔵。空白のままなら推奨デフォルトに自動フォールバック、入力した分だけ適用され、誤設定・漏れ設定を防止。",
              "val": "監視種別ごとに自社基準でアラート"
            },
            {
              "title": "重要度分類とノイズ削減",
              "color": "warn",
              "icon": "M3 12h4l3-9 4 18 3-9h4",
              "desc1": "重大／警告の2段階。イベントの重複排除とクーリング（5分以内の同一イベントは再送しない）によりノイズを抑制、アラート量を80%削減。",
              "val": "本当の障害が埋もれなくなる"
            },
            {
              "title": "オフライン即時アラート",
              "color": "warn",
              "icon": "M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z",
              "desc1": "Agentが30秒報告無き場合、即座に重大オフラインアラートを発報。分散環境下でもホストの孤立を見逃さない。",
              "val": "マシンの障害を同僚より早く検知"
            },
            {
              "title": "アラートサイレンス",
              "color": "warn",
              "icon": "M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9",
              "desc1": "メンテナンス窓口や既知のイベント中はワンクリックでサイレンスし、無駄な通知を防止。サイレンス終了後は自動復帰。",
              "val": "メンテナンス中も無駄なアラートに悩まされない"
            },
            {
              "title": "アラートルーティング",
              "color": "accent",
              "icon": "M17 1l4 4-4 4 M3 11V9a4 4 0 0 1 4-4h14 M7 23l-4-4 4-4 M21 13v2a4 4 0 0 1-4 4H3",
              "desc1": "タグ／重要度でアラートを異なるチャネルと受信グループに振り分け、ビジネスアラートとインフラアラートを適切に分離。",
              "val": "正しいアラートを正しい担当者へ"
            }
          ]
        },
        {
          "tag": "03",
          "title": "リモートアクセスと監査",
          "desc": "ポートを開けずに到達でき、事後にすべて追跡可能",
          "pain": "機房のネットワーク分離で現場に行けず、事後も説明がつかない？",
          "roles": [
            "運用",
            "セキュリティ",
            "監査"
          ],
          "items": [
            {
              "title": "リモートターミナル",
              "color": "ok",
              "icon": "M4 17l6-6-6-6M12 19h8",
              "desc1": "ブラウザから直接ホストのターミナルに接続。Agentのリバース接続でインバウンドポート不要。マルチタブ、ウィンドウ自動適応、完全なVT100エミュレーション（vim/topの全画面対応）、モバイル仮想キーボード。",
              "val": "VPN＋SSH不要、ブラウザで直接接続"
            },
            {
              "title": "ターミナルセッションの再生",
              "color": "ok",
              "icon": "M23 4v6h-6M1 20v-6h6M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15",
              "desc1": "すべてのセッションを録画（タイムスタンプ付きフレーム）。1x/2x/4x/8xの倍速再生。リアルタイム同時観覧で複数人が同一セッションを同時確認。一覧は操作者／ホスト／IPの3軸で検索可能。",
              "val": "誰がいつ何をしたか、完全に追跡可能"
            },
            {
              "title": "ポート転送（TCP / UDP / HTTP）",
              "color": "ok",
              "icon": "M4 12v8a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-8M16 6l-4-4-4 4M12 2v13",
              "desc1": "Agentのリバーストンネルで内網のWeb／DB／デバッグインターフェースをローカルブラウザにマップし、パブリック公開なし。TCP / UDP / HTTPの3プロトコル単ポート転送と、TCP / UDPのポート範囲一括転送（1バッチ最大100連続ポート、同グループ一括開始停止削除）に対応。",
              "desc2": "一覧／カードの両视图。有効／無効／複製／編集／削除をワンクリック。転送統計とヘルスチェックAPIで異常をリアルタイム検知。",
              "val": "内網サービスを手軽にローカルからアクセス"
            },
            {
              "title": "操作ログと監査",
              "color": "ok",
              "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8M16 17H8M10 9H8",
              "desc1": "全量の操作ログ（操作／システム／プラグインの3種）を収集し、絞り込みとCSVエクスポートに対応。ターミナル録画・コマンド監査と連動し、完全な監査クローズドループを構成。",
              "val": "コンプライアンス監査の証跡をワンクリック出力"
            }
          ]
        },
        {
          "tag": "04",
          "title": "自動化と自己修復",
          "desc": "繰り返し作業をプレイブックに任せ、一般的な障害を自動でクローズ",
          "pain": "同じ障害を夜中に繰り返し対応し、手作業のプレイブックは遅くてミスがち？",
          "roles": [
            "運用",
            "SRE"
          ],
          "items": [
            {
              "title": "自動化プレイブックのオーケストレーション",
              "color": "purple",
              "icon": "M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z",
              "desc1": "コマンド列をビジュアルに定義し、複数ホストへワンクリック一括実行。実行結果をリアルタイムで返送し、成功／失敗の集計とステップ単位の出力に対応。",
              "desc2": "実行履歴を完全に保持。操作者・時刻・結果はすべて追跡可能。",
              "val": "100台のホストへ一括パッチ適用を10分で完了"
            },
            {
              "title": "アラート→プレイブック自動修復",
              "color": "purple",
              "icon": "M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z",
              "desc1": "ルール一致後に自動で修復プレイブックを起動。クーリング窓＋毎時レート制限＋任意の人的承認という3重のガードレールで、揺れアラートによる修復の雪だるまを防止。",
              "desc2": "各自動修復のルール・プレイブック・結果をすべて記録し、監査・追跡可能。",
              "val": "一般的な障害は自己修復、人は本当に困難なものだけに対応"
            },
            {
              "title": "SLOとエラーバジェット",
              "color": "accent",
              "icon": "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8z",
              "desc1": "メトリクスまたはプローブに基づき可用性目標を定義。長期窓のSLIはVictoriaMetricsから直接計算し、エラーバジェットの枯渇でインシデント予警報を自動発報。",
              "val": "勘ではなくデータでSLAを語る"
            },
            {
              "title": "チケットフロー",
              "color": "ok",
              "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8M16 17H8M10 9H8",
              "desc1": "インシデントをワンクリックでチケットに昇格。状態遷移＋コメント＋割り当てを完全に記録し、対応責任を明確化。",
              "val": "検知からクローズまで、プロセス全体を追跡"
            },
            {
              "title": "インシデントのクローズドループ",
              "color": "ok",
              "icon": "M23 4v6h-6M1 20v-6h6M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15",
              "desc1": "アラート／SLO枯渇／手動をすべてインシデントに集約し、完全なタイムライン（発報→確認→修復→復旧）を付与。同一根因のホスト間重複を統合し、画面いっぱいの重複アラートを排除。",
              "val": "障害に主线ができ、各々がバラバラなアラートを見なくて済む"
            }
          ]
        },
        {
          "tag": "05",
          "title": "ログとインサイト",
          "desc": "メトリクスが赤くなったら、ログがその理由を教える",
          "pain": "メトリクス異常なのに根因が見つからず、当てずっぽう？",
          "roles": [
            "運用",
            "SRE",
            "開発"
          ],
          "items": [
            {
              "title": "Agentの増分収集",
              "color": "accent",
              "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6",
              "desc1": "--log-pathsで収集対象のログファイルを指定。ファイル末尾から増分追跡し、一括圧縮で報告。再起動後も継続送信で重複・欠落なし。",
              "val": "アプリログが自動集約、マシンに入ってtailする必要なし"
            },
            {
              "title": "全文検索",
              "color": "accent",
              "icon": "M21 21l-5.2-5.2M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0z",
              "desc1": "ホスト／レベル（error・warn・info）／キーワード／期間の組み合わせで検索。ログは自動でレベル別に着色され、ヒット即特定。",
              "val": "数十台のログを1つの検索窓で網羅"
            },
            {
              "title": "インシデントとの連動",
              "color": "ok",
              "icon": "M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71",
              "desc1": "インシデント詳細にそのホストの直近1時間のerror／warnログを自動関連付け。メトリクス異常とログの現場を並べて表示し、切り替え不要でトラブルシュート。",
              "val": "メトリクスがどこがおかしいか、ログがなぜかを提示"
            },
            {
              "title": "軽量AI異常検知",
              "color": "purple",
              "icon": "M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z",
              "desc1": "プラグイン内蔵のz-score軽量異常検知で、別途機械学習基盤なしにメトリクスの急変を検出。",
              "val": "異常を自動で赤枠表示、グラフを監視する必要なし"
            }
          ]
        },
        {
          "tag": "06",
          "title": "AI運用アシスタント",
          "desc": "巡検・診断・自律エージェントで、経験を自動研判に沈殿",
          "pain": "トラブルシュートはベテランの経験任せ、新人がカバーできず深夜に見張りがいない？",
          "roles": [
            "運用",
            "SRE",
            "管理者"
          ],
          "items": [
            {
              "title": "定期AI巡検",
              "color": "purple",
              "icon": "M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z",
              "desc1": "定期的なヘルス巡検で、オンライン率・発報中アラート・SLO超過・リソース高位・エラーログ急増を自動集約。リスク発見時のみ通知し、健康時は邪魔しない。",
              "val": "誰も見張らない深夜も、AIが巡検"
            },
            {
              "title": "インシデント根因診断",
              "color": "purple",
              "icon": "M22 12h-4l-3 9L9 3l-3 9H2",
              "desc1": "インシデントに対しワンクリックで根因を研判し、可能性順の根因仮説＋実行可能な対処ステップを出力。結果は自動でインシデントタイムラインに書き込まれ、新規重大インシデントは自動で診断を起動。",
              "val": "トラブルシュートが「経験」から「手がかり」に"
            },
            {
              "title": "エージェント＋ヒューリスティック代替",
              "color": "accent",
              "icon": "M9 3v2M15 3v2M9 19v2M15 19v2M3 9h2M3 15h2M19 9h2M19 15h2M9 9h6v6H9z",
              "desc1": "AI Provider（LLM）を設定時はエージェント級の分析を実行。未設定時はヒューリスティックルールで代替し、決して空回りしない。error／warnログを自動で分析コンテキストに組み込み、現場に即した判断を実現。",
              "val": "大規模モデルがあれば賢く、なくても使える"
            },
            {
              "title": "自律エージェント",
              "color": "purple",
              "icon": "M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z",
              "desc1": "内蔵の自律Agentフレームワーク（外部ゲートウェイ非依存）がFunction Callingで診断／照会／修復能力を呼び出し、「質問」から「動ける運用アシスタント」へ昇華。",
              "val": "提案だけでなく、対処の実行も可能"
            },
            {
              "title": "RAG診断ナレッジベース",
              "color": "accent",
              "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6",
              "desc1": "pgvectorを用い、過去のアラート／インシデント／ログを検索可能な診断ベクトル庫に沈殿。根因研判と巡検結論を真の障害現場に即して提示。",
              "val": "使うほど自システムを理解する"
            },
            {
              "title": "ベクトルモデルの自由設定",
              "color": "accent",
              "icon": "M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20z M2 12h20 M12 2a15 15 0 0 1 0 20 M12 2a15 15 0 0 0 0 20",
              "desc1": "ベクトル化（embedding）モデルと対話モデルを分離し、任意のOpenAI互換／embeddings——OpenAI、Alibaba Bailian、または自建のbge／m3e／gteローカルモデル——を指定可能。エンドポイント／キー／モデル／次元を独立設定。",
              "desc2": "対話には大規模モデル、ベクトルには軽量モデルを使い分け、それぞれ課金・レート制限。ワンクリックで接続性をテストし実際の次元を表示、設定不一致をその場で検出。",
              "val": "RAGはベンダーロックフリー、ローカル私有化でも動作"
            }
          ]
        },
        {
          "tag": "07",
          "title": "セキュリティ・コンプライアンスと高可用性",
          "desc": "プライベート導入で、セキュリティ・コンプライアンス・信頼性をすべてカバー",
          "pain": "データは内網から出せない、セキュリティとコンプライアンスは誰が保証し、本番に乗せられるか？",
          "roles": [
            "セキュリティ",
            "管理者",
            "運用"
          ],
          "items": [
            {
              "title": "マルチユーザーRBAC",
              "color": "purple",
              "icon": "M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2 M9 7a4 4 0 1 0 0 8 4 4 0 0 0 0-8z M23 21v-2a4 4 0 0 0-3-3.87",
              "desc1": "3階層のロール：管理者（全操作）、オペレーター（ターミナル＋アラート）、ビューア（読み取り専用）。ルート単位の権限遮断＋ユーザー管理画面。",
              "val": "人によって異なる能力の境界"
            },
            {
              "title": "MFA二段階認証",
              "color": "purple",
              "icon": "M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5",
              "desc1": "TOTP二段階認証（RFC 6238、Google Authenticator互換）。ログインと重要操作の二段確認。",
              "val": "アカウント漏洩でも侵入不可"
            },
            {
              "title": "アカウント復旧",
              "color": "purple",
              "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6",
              "desc1": "ユーザー名忘れ／パスワード忘れ（メール認証コード）／メールによるMFA解除をサポート。列挙攻撃対策を全過程で保護。",
              "val": "管理者退職でもロックアウトしない"
            },
            {
              "title": "マシン指紋認証",
              "color": "purple",
              "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4",
              "desc1": "machine-id＋MACハッシュの指紋バインド。Agent端末チャネルは指紋で認証（トークン非依存）。トークンローテーションは既存Agentに影響せず、7日間の猶予期間。",
              "val": "トークンローテーションで既存Agentを中断しない"
            },
            {
              "title": "コンプライアンス監査クローズドループ",
              "color": "purple",
              "icon": "M9 2h6a2 2 0 0 1 2 2v0h3a2 2 0 0 1 2 2v13a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h3a2 2 0 0 1 2-2z M9 14l2 2 4-4",
              "desc1": "ターミナル録画再生＋操作ログ＋MFA＋RBACで、監査の「追跡可能・管理可能・記録あり」要件を満たす。",
              "val": "コンプライアンス監査を強力に支援"
            },
            {
              "title": "データ永久保存",
              "color": "accent",
              "icon": "M3 3v18h18M3 13l4-4 3 3 5-6",
              "desc1": "5秒間隔の生メトリクスを永久保存、自動期限切れ・自動削除なし。高圧縮保存で長期遡及も負担なし。",
              "val": "任意の過去時点をいつでも遡及"
            },
            {
              "title": "マルチサーバー配信とクロスDC DR",
              "color": "ok",
              "icon": "M12 2L2 7v10l10 5 10-5V7L12 2z",
              "desc1": "単一Agentが複数サーバーへ同時配信。各端が独立した認証と再試行を持ち、DRやチーム間共有監視に適す。",
              "val": "1回の収集で複数の保障"
            }
          ]
        },
        {
          "tag": "08",
          "title": "シンプル導入とオープンエコシステム",
          "desc": "1つのバイナリで起動し、既存のツールチェーンも受け止める",
          "pain": "監視を導入するのに複数コンポーネントと数日が必要、既存チェーンも置換できない？",
          "roles": [
            "運用",
            "管理者",
            "開発"
          ],
          "items": [
            {
              "title": "単一バイナリ・PG+VMストレージ",
              "color": "accent",
              "icon": "M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z",
              "desc1": "サーバー／Agentともに単一Goバイナリ、サードパーティ依存ゼロ。時系列データはVictoriaMetricsに、関係データはPostgreSQLに統合。docker composeで一式をワンコマンド起動。",
              "val": "サーバー＋PG＋VMをcomposeでワンコマンド"
            },
            {
              "title": "インストールウィザード",
              "color": "accent",
              "icon": "M12 3v12M7 10l5 5 5-5 M5 21h14",
              "desc1": "docker-compose.ymlをダウンロードし1コマンドでサーバー起動（キー自動生成で書き込み）。install.shがCPUアーキテクチャ（AMD64/ARM64）を自動検出し対応Agentバイナリをダウンロード、1本のcurlでインストール完了。",
              "val": "運用初心者でも3分で本番開始"
            },
            {
              "title": "ゲートウェイ中継モード",
              "color": "accent",
              "icon": "M17 1l4 4-4 4 M3 11V9a4 4 0 0 1 4-4h14 M7 23l-4-4 4-4 M21 13v2a4 4 0 0 1-4 4H3",
              "desc1": "内網で1台だけがクラウドへ接続し、すべての要求を代理。バイナリ／報告／ターミナルが自動で透過。サブネット超えやファイアウォール裏のホストに適す。",
              "val": "每台のマシンに外網を開く必要なし"
            },
            {
              "title": "PWAオフラインアクセス",
              "color": "accent",
              "icon": "M12 18h.01M8 21h8a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2H8a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2z M9 7h.01",
              "desc1": "デスクトップにインストール可能（PWA）、独立ウィンドウで動作。App Shellのオフラインキャッシュで、ネットワーク断でも最後の既知状態を表示。",
              "val": "スマホにもインストール可、いつでもどこでも確認"
            },
            {
              "title": "PythonプラグインSDK",
              "color": "purple",
              "icon": "M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4",
              "desc1": "内蔵プラグインSDKで数行のコードでカスタムメトリクス（MySQL接続数、Nginxリクエスト数、Redisメモリなど）を収集。",
              "desc2": "内蔵サンプル：プロセス監視、サービスポート生存確認など。",
              "val": "何を監視するかはあなた次第"
            },
            {
              "title": "インシデントWebhook外部送信",
              "color": "accent",
              "icon": "M22 2L11 13M22 2l-7 20-4-9-9-4 20-7z",
              "desc1": "アラートとインシデントをあなたのチケットシステム・IM・自社プラットフォームへ送信し、既存の運用フローと連携。単一画面に閉じ込める必要なし。",
              "val": "アラートが既存のチケットフローに直結"
            },
            {
              "title": "Feishu / DingTalk / メールのネイティブ統合",
              "color": "warn",
              "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6",
              "desc1": "5つの通知チャネルをすぐに利用可能（Feishu / DingTalk / メール / SMS / 音声通話）。DingTalk WebhookはHMAC署名検証付きで、配送は便利かつ安全。",
              "val": "チームの常用コラボツールがそのままアラート受信"
            },
            {
              "title": "可観測データの開放",
              "color": "accent",
              "icon": "M3 3v18h18M7 14l4-4 3 3 5-6",
              "desc1": "時系列データはVictoriaMetrics基盤でPrometheusリモート読み書きプロトコル互換。既存のダッシュボード／アラートチェーンと共存可能。",
              "val": "技術スタックを置換せず、短板だけを補完"
            }
          ]
        },
        {
          "tag": "09",
          "title": "ハードウェア巡検とストレージ収集",
          "desc": "サーバーハードウェアからストレージアレイまで、全スタックの資産と状態を可視化",
          "pain": "サーバーハードウェア資産は手作業登録、ストレージアレイのアラートはベンダーツール頼みで、ホスト監視と分断？",
          "roles": [
            "運用",
            "IT資産管理",
            "ストレージ管理者"
          ],
          "items": [
            {
              "title": "Redfishハードウェア巡検",
              "color": "accent",
              "icon": "M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4",
              "desc1": "標準Redfish/DMTFプロトコルでサーバーハードウェア資産をリモート収集：プロセッサ／メモリ／ディスク／RAID／NIC／ファン／電源／温度。Agent不要。",
              "desc2": "Huawei iBMCの深い互換（ProcessorView／MemoryViewを一括収集）に対応し、TaiShan／Kunpengシリーズに適合。",
              "val": "ハードウェア資産の自動発見、手作業登録から解放"
            },
            {
              "title": "OceanStorストレージ収集",
              "color": "accent",
              "icon": "M2 20h20M5 20V10l7-6 7 6v10M9 20v-4h6v4",
              "desc1": "Huawei OceanStor RESTful API経由でストレージプール／LUN／コントローラ／ディスク／アラートなどの資産と性能データを収集し、統一監視パネルに組み込む。",
              "val": "ストレージアレイとホスト資産を同一パネルで確認"
            },
            {
              "title": "NetFlowトラフィック分析",
              "color": "accent",
              "icon": "M3 12h4l3-9 4 18 3-9h4",
              "desc1": "内蔵のNetFlow v5/v9/IPFIX収集器で、5組（送信元／宛先IP＋ポート＋プロトコル）トラフィック統計とTOP-Nランキング、トラフィックヒートマップを可視化。",
              "desc2": "flow_recordsは日次自動パーティションで、期限切れは自動クリアし単表の肥大化を防止。",
              "val": "異常トラフィックを一目で特定、帯域滥用を暴く"
            },
            {
              "title": "ハードウェア資産の統一エクスポート",
              "color": "purple",
              "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8 M16 17H8 M10 9H8",
              "desc1": "ハードウェア巡検結果をMarkdown／Excel／Word／PDFの4形式でエクスポート。サードパーティ依存ゼロで、資産監査と報告に便利。",
              "val": "巡検レポートをワンクリック出力、資産監査も安心"
            }
          ]
        },
        {
          "tag": "10",
          "title": "モバイル運用",
          "desc": "スマホの中の運用センター、いつでもどこでも全体を掌握",
          "pain": "パソコンの前にいなければアラートが見えず、ホストを調べられず、ターミナルに入れない？",
          "roles": [
            "運用",
            "当番",
            "管理者"
          ],
          "items": [
            {
              "title": "ネイティブAndroidアプリ",
              "color": "ok",
              "icon": "M7 2h10a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2z M12 18h.01",
              "desc1": "Kotlin＋Jetpack Composeによるネイティブ開発。WebViewの外装ではない。Material 3デザイン言語、ダーク／ライトテーマ自動追従。",
              "desc2": "ホスト概要／アラートプッシュ／リモートターミナル／ハードウェアレポートを、複数ホストでスムーズに切り替え。",
              "val": "ネイティブで快適な体験、スマホも運用の武器"
            },
            {
              "title": "PWAモバイル対応",
              "color": "ok",
              "icon": "M12 18h.01M8 21h8a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2H8a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2z M9 7h.01",
              "desc1": "Webパネルをスマホのホーム画面にインストール可能（PWA）、独立ウィンドウで動作。App Shellオフラインキャッシュで、ネットワーク断でも最後の既知状態を表示。",
              "val": "アプリを入れずとも使える、ブラウザが入り口"
            },
            {
              "title": "プライベート自ホスティング",
              "color": "accent",
              "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z",
              "desc1": "サーバーアドレスをカスタム。データは常に内網に留まり、第三者サービスを経由しない。ログイン資格情報はWeb側と同一のRBACアカウント体系を共有。",
              "val": "データは内網から出ず、セキュリティ・コンプライアンスも安心"
            }
          ]
        }
      ],
      "cta.title": "これらの機能、すべて一つのバイナリに含まれます",
      "cta.desc": "Prometheus ＋ Grafana ＋ Alertmanager ＋ 踏み台 ＋ チケットシステムを別々に組む必要はもうありません —— AIOps が一つで完結。",
      "cta.btn2": "ソリューションを見る"
    }
  },
  "solutions": {
    "ja": {
      "page.title": "ソリューション — AIOps",
      "page.desc": "単一ホスト・マルチDC・チーム・コンプライアンスごとにZabbix＋Prometheus＋Grafanaを別々に組む必要はもうありません。AIOps は同一のプライベート自ホスティング基盤で、最も頭を悩ませる7つの実運用シナリオをスムーズにカバー —— コマンド一つで導入、データは永久に自社保有。",
      "page.oglocale": "ja_JP",
      "head.tag": "ソリューション",
      "head.title": "実シナリオ、実価値",
      "head.desc": "単一ホストからマルチDC、障害対応からコストガバナンスまで、真の運用シナリオをカバー",
      "cta.title": "あなたの運用シナリオが何であれ",
      "cta.desc": "単一ホストでもマルチDCでも、チーム協働やコンプライアンス監査でも —— AIOps なら3分で完了。",
      "cta.btn1": "無料で導入 →",
      "cta.btn2": "製品比較を見る",
      "scenarios": [
        {
          "num": "シナリオ 01",
          "title": "単一ホスト監視のクイックデプロイ",
          "result": "プライベート導入を3分で完了・商用ライセンス費用ゼロ・ホスト数無制限",
          "desc": "1台のサーバーがビジネスを稼働し、1人で運用を担当。専任の監視チームもなく、商用APMを買う予算もない。",
          "points": [
            "docker-compose.ymlをダウンロード、1コマンドでキーを自動生成して書き込み",
            "docker composeでサーバー＋PostgreSQL＋VictoriaMetricsをワンコマンド起動",
            "3分以内で導入完了、ブラウザで即利用",
            "1本のcurlコマンドで対象ホストにAgentをインストール（アーキテクチャ自動検出）",
            "Feishu/DingTalk Webhookを設定するだけでアラート受信"
          ],
          "visual": "<span style=\"color:var(--muted)\"># 1. GitHub経由でダウンロード</span><br>bash &lt;(curl -fsSL https://raw.githubusercontent.com/sreyun/aiops-monitor/master/scripts/secure-compose.sh)<br><span style=\"color:var(--muted)\"># Giteeミラー経由（GitHubが遅い場合推奨）</span><br>bash &lt;(curl -fsSL https://gitee.com/bigdatasafe/aiops-monitor/raw/master/scripts/secure-compose.sh)<br><span style=\"color:var(--muted)\"># 2. 起動（キー書き込み済み、手動編集不要）</span><br>docker compose up -d<br><span style=\"color:var(--muted)\"># 3. 対象ホストにAgentをインストール（任意、アーキテクチャ自動検出）</span><br>curl -fsSL \"http://localhost:8529/install.sh?token=XXX\" | sudo sh<br><br><span style=\"color:var(--ok)\">✓ ブラウザで http://localhost:8529 を開く</span><br><span style=\"color:var(--muted)\">デフォルト認証 admin / admin（初回ログインで変更必須）</span>"
        },
        {
          "num": "シナリオ 02",
          "title": "マルチDCの集中監視",
          "result": "1000台超のホストを1画面で管理・障害検知を30分から30秒へ",
          "desc": "複数のDC、数十～数百台のマシン。分散管理では全体像が見えず、どこかが落ちても半日気づかない。",
          "points": [
            "すべてのホストを単一パネルに統合、分類でグループ表示",
            "Agentがマルチサーバー配信、クロスDC DRでデータ消失なし",
            "ゲートウェイ中継モード：サブネット超え／ファイアウォール裏のホストも統合",
            "オフラインアラート：30秒報告無きホストは即重大アラート",
            "概要KPIカード：オンライン／オフライン／重大／警告が一目"
          ],
          "visual": "<span style=\"color:var(--accent2)\">[概要]</span> 15台のホスト · 14オンライン · 1オフライン<br><span style=\"color:var(--ok)\">●</span> web-01    CPU 23%  MEM 45%<br><span style=\"color:var(--ok)\">●</span> web-02    CPU 18%  MEM 52%<br><span style=\"color:var(--ok)\">●</span> db-master  CPU 67%  MEM 81%<br><span style=\"color:var(--crit)\">●</span> db-slave   CPU  0%  MEM  0%<br><span style=\"color:var(--warn)\">⚠</span> アラート: db-slave が120秒間孤立<br><span style=\"color:var(--accent2)\">[ターミナル]</span> db-slave をクリック → リモート調査"
        },
        {
          "num": "シナリオ 03",
          "title": "チーム協働運用",
          "result": "操作100%追跡可能・事故の責任特定が「言い合い」から「証拠」に",
          "desc": "複数人で運用しているが踏み台がなく、誰がどのマシンに入り何をしたか一切記録なし。問題が起きると互いに責任の押し付け合い。",
          "points": [
            "3階層RBAC：管理者／オペレーター／ビューアの権限分離",
            "リモートターミナル全程録画、倍速再生で追跡",
            "操作ログ：誰がいつどのホストで何をしたか",
            "リアルタイム同時観覧：複数人が同一セッションを同時確認",
            "MFA二段階認証＋ブルートフォース防護"
          ],
          "visual": "<span style=\"color:var(--accent2)\">[ターミナルセッション]</span><br>操作者: <span style=\"color:var(--ok)\">zhangsan</span><br>ホスト: db-master (10.0.1.5)<br>時刻: 14:23:08<br><span style=\"color:var(--muted)\">─ コマンド監査 ─</span><br>$ top<br>$ systemctl restart nginx<br>$ tail -f /var/log/error.log<br><span style=\"color:var(--ok)\">✓ セッション録画済・再生可能</span>"
        },
        {
          "num": "シナリオ 04",
          "title": "等級保護（等保）コンプライアンス監査",
          "result": "コンプライアンス監査の証跡作成を数週間から数時間へ・操作/権限/アラート全記録",
          "desc": "等級保護の評価では、操作の追跡可能性・権限の管理可能性・アラートの記録が求められる。従来の手作業ログ整理は時間と労力がかかる。",
          "points": [
            "全量操作ログ：操作／システム／プラグインの3種、絞り込みとCSVエクスポートに対応",
            "ターミナルセッション録画＋再生で操作の追跡要件を満たす",
            "MFA＋RBACでアクセス制御要件を満たす",
            "アラート配信記録の監査可能（Feishu/DingTalk/メール/SMS/音声通話）",
            "PG＋VM統合ストレージ：監査と履歴データが再起動でも消失しない"
          ],
          "visual": "<span style=\"color:var(--accent2)\">[操作ログ]</span><br>14:23  操作  zhangsan  リモートターミナルを開く<br>14:25  操作  zhangsan  端末コマンド: systemctl restart<br>14:30  システム  アラートエンジン  CPU正常化<br>14:31  システム  通知エンジン  Feishu配信: アラート復旧<br>15:00  操作  lisi      プレイブック実行: 一括パッチ更新<br>15:05  操作  lisi      プレイブック完了: 12/12 成功<br><span style=\"color:var(--muted)\">─ CSVエクスポート可 ─</span>"
        },
        {
          "num": "シナリオ 05",
          "title": "障害対応と夜間当番",
          "desc": "深夜のアラートで起こされるが、席を離れておりどこから調べるべきかも不明。従来の「pingが通ればよい」監視では問題発生時にすべて手作業、MTTRは軽く数時間。",
          "points": [
            "重大／警告の2段階アラート＋スマート重複排除クーリングでアラート爆撃と疲労を防止",
            "スマホのブラウザから直接開き、リモートターミナルが障害ホストに秒単位で接続",
            "AI運用アシスタント：巡検診断で根因を特定、自律エージェントが対処提案を出し直接実行可能、RAGナレッジベースが過去の経験を沈殿",
            "内蔵の通知履歴：誰がいつどのアラートを受けたか完全追跡",
            "ワンクリックプレイブックで一般的な止血操作（サービス再起動／ディスク清理／プロセス再開）"
          ],
          "visual": "<span style=\"color:var(--warn)\">⚠ 02:14 重大アラート  web-02 CPU 98%</span><br><span style=\"color:var(--muted)\"># スマホでパネルを開き、秒単位で接続</span><br>$ top → javaプロセスがCPUを100%占有<br><span style=\"color:var(--accent2)\">[AI運用アシスタント]</span> 根因: キャッシュ崩壊 → 接続プール枯渇<br><span style=\"color:var(--muted)\"># 止血プレイブックをワンクリック実行</span><br>$ プレイブック実行: gateway再起動 + キャッシュ予熱<br><span style=\"color:var(--ok)\">✓ 02:17 復旧・MTTR 3分</span>",
          "result": "MTTRを平均2時間から15分へ・夜間の無効アラートを80%削減"
        },
        {
          "num": "シナリオ 06",
          "title": "Webサイトとビジネス可用性監視",
          "desc": "公式サイト／ミニプログラムが数時間ダウンしても、気づいたのは顧客。7×24の能動的プローブがなく、SSL証明書期限切れ・インターフェース5xx・DNS異常はすべて後手。",
          "points": [
            "HTTP／ICMPの能動プローブで、サーバー側視点から可用性を継続チェック",
            "SSL証明書期限切れの事前警告（デフォルト30日前）",
            "インターフェースのステータスコード／応答時間／キーワードアサーション、期待から外れればアラート",
            "ビジネスAPIプローブ（apimon）：コア業務インターフェースのステータスコード／応答内容をアサートし、単なる死活より真のユーザー体験に近い",
            "プローブデータを同一パネルに取り込み、ホストメトリクスと連動分析",
            "Feishu／DingTalk／メール／SMS／音声通話のマルチチャネル配信で障害をいち早く通知"
          ],
          "visual": "<span style=\"color:var(--accent2)\">[可用性プローブ]</span><br><span style=\"color:var(--ok)\">●</span> https://api.example.com   200  48ms<br><span style=\"color:var(--ok)\">●</span> https://shop.example.com  200  62ms<br><span style=\"color:var(--crit)\">●</span> https://pay.example.com  503  タイムアウト<br><span style=\"color:var(--warn)\">⚠</span> SSL: pay.example.com 残り6日<br><span style=\"color:var(--muted)\">─ 直近24h 可用率 ─</span><br>api 100% · shop 99.98% · pay 98.20%",
          "result": "障害検知を「顧客苦情」から「秒級プローブ」へ・オンライン可用率99.9%達成"
        },
        {
          "num": "シナリオ 07",
          "title": "コスト最適化とリソースガバナンス",
          "desc": "クラウド請求が月々膨らむが、どのマシンが空転しどのインスタンスを縮小すべきか説明できない。利用率はブラックボックス、予算は勘。",
          "points": [
            "CPU／メモリ／ディスク／トラフィックの長期傾向で遊休と過負荷を識別",
            "低水位インスタンスを自動標示し、縮小／統合の提案",
            "キャパシティ予測で拡張決定を支援、むやみなオーバープロビジョニングを回避",
            "マルチDCのリソース集計比較でゾンビ資産を一目",
            "履歴曲線でコスト振り返りと翌月予算策定を支援"
          ],
          "visual": "<span style=\"color:var(--accent2)\">[リソース利用率 · 直近30日]</span><br>web-01   CPU 12%  MEM 30%  <span style=\"color:var(--warn)\">低水位</span><br>web-02   CPU 15%  MEM 28%  <span style=\"color:var(--warn)\">低水位</span><br>db-01    CPU 71%  MEM 84%  <span style=\"color:var(--ok)\">正常</span><br>cache-01 CPU  3%  MEM 12%  <span style=\"color:var(--crit)\">ゾンビ</span><br><span style=\"color:var(--muted)\">─ 最適化提案 ─</span><br>web-01/02を統合 · cache-01を回収<br><span style=\"color:var(--ok)\">月額約¥1,820削減見込み</span>",
          "result": "遊休リソースを特定・回収し、クラウドコストを平均20–35%削減"
        }
      ]
    }
  },
  "pricing": {
    "zh-CN": {
      "page.title": "定价方案 — AIOps",
      "page.desc": "核心平台永久免费（AGPL-3.0 协议），企业可按需选购技术支持服务。一套代码，按需解锁支持层级。"
      "page.oglocale": "zh_CN",
      "heroTag": "定价方案",
      "heroTitle": "开源免费，按需选择支持",
      "heroDesc": "核心平台永久免费（AGPL-3.0 协议），企业可按需选购技术支持服务。一套代码，按需解锁支持层级。"
      "recommendLabel": "推荐",
      "colCommunity": "社区版",
      "colStandard": "标准支持",
      "colEnterprise": "企业支持",
      "plans": [
        {
          "name": "社区版",
          "price": "¥0",
          "unit": "永久免费",
          "highlight": true,
          "desc": "完整核心功能，适合个人与中小团队自建私有化监控。",
          "cta": "免费部署 →",
          "ctaHref": "https://github.com/sreyun/aiops-monitor",
          "features": [
            {
              "t": "全部监控 / 告警 / 终端 / 自动化能力",
              "ok": true
            },
            {
              "t": "PostgreSQL + VictoriaMetrics 双引擎存储",
              "ok": true
            },
            {
              "t": "Android / PWA 移动端",
              "ok": true
            },
            {
              "t": "社区支持（GitHub Issues）",
              "ok": true
            },
            {
              "t": "优先邮件响应（1–2 工作日）",
              "ok": false
            },
            {
              "t": "专属解决方案架构师",
              "ok": false
            },
            {
              "t": "定制开发 / 私有化实施",
              "ok": false
            }
          ],
          "bestFor": "个人开发者 / 中小团队 / 技术选型验证"
        },
        {
          "name": "标准支持",
          "price": "按规模报价",
          "unit": "／年",
          "highlight": false,
          "desc": "为已上生产的团队提供稳定保障。",
          "cta": "联系销售",
          "ctaHref": "contact.html",
          "features": [
            {
              "t": "包含社区版全部功能",
              "ok": true
            },
            {
              "t": "优先邮件响应（1–2 工作日）",
              "ok": true
            },
            {
              "t": "部署咨询与最佳实践",
              "ok": true
            },
            {
              "t": "社区支持",
              "ok": true
            },
            {
              "t": "专属解决方案架构师",
              "ok": false
            },
            {
              "t": "定制开发 / 私有化实施",
              "ok": false
            }
          ],
          "bestFor": "已上线生产、需要稳定保障的成长型团队"
        },
        {
          "name": "企业支持",
          "price": "定制报价",
          "unit": "／年",
          "highlight": false,
          "desc": "面向大型组织、合规与大规模落地场景。",
          "cta": "联系销售",
          "ctaHref": "contact.html",
          "features": [
            {
              "t": "包含标准支持全部能力",
              "ok": true
            },
            {
              "t": "专属解决方案架构师",
              "ok": true
            },
            {
              "t": "私有化实施 + 定制开发",
              "ok": true
            },
            {
              "t": "培训与合规落地支持",
              "ok": true
            },
            {
              "t": "大规模滚动升级保障",
              "ok": true
            }
          ],
          "bestFor": "大型企业 / 强合规 / 多机房大规模落地"
        }
      ],
      "compareTitle": "功能对比",
      "compareDesc": "同一套开源代码，按支持层级解锁服务。",
      "matrix": {
        "groups": [
          {
            "group": "监控与告警",
            "rows": [
              {
                "label": "实时指标 / GPU / 拨测",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "多级阈值与降噪",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "远程终端 + 会话回放",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "自动化剧本 / 自愈",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "AI 巡检 / 根因诊断",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              }
            ]
          },
          {
            "group": "支持与服务",
            "rows": [
              {
                "label": "社区支持（GitHub）",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "优先邮件响应",
                "c": "—",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "部署咨询 / 最佳实践",
                "c": "—",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "专属解决方案架构师",
                "c": "—",
                "s": "—",
                "e": "✓"
              },
              {
                "label": "定制开发 / 私有化实施",
                "c": "—",
                "s": "—",
                "e": "✓"
              },
              {
                "label": "培训与合规落地",
                "c": "—",
                "s": "—",
                "e": "✓"
              }
            ]
          }
        ]
      },
      "ctaTitle": "准备好开始了吗？",
      "ctaDesc": "社区版永久免费，一条命令私有化部署。",
      "ctaBtn1": "免费部署 →",
      "ctaBtn2": "查看功能",
      "footNote": "所有版本均基于同一套开源代码，数据永久自持、私有化部署，绝无功能降级或自动过期。",
            "whyFree.tag": "开源商业模式", "whyFree.title": "为什么核心平台永久免费？", "whyFree.desc": "我们相信好的基础设施软件应该人人可用，商业化不靠功能阉割，而靠服务增值。",
            "whyFree.i1title": "AGPL-3.0 协议，代码全公开", "whyFree.i1desc": "所有核心功能 100% 开源，无隐藏模块、无功能阉割。你拿到的代码和社区完全一致。",
            "whyFree.i2title": "社区驱动，用户即产品主人", "whyFree.i2desc": "功能优先级由社区投票与真实需求决定，不被资本裹挟，不做只为付费客户定制的“企业特供版”。",
            "whyFree.i3title": "靠服务增值，不靠功能收费", "whyFree.i3desc": "专业版 / 企业版的价值在于技术支持、SLA 保障与定制咨询，而非把免费功能锁起来再卖一次。",
            "pricingFaq.tag": "定价 FAQ", "pricingFaq.title": "关于定价，你可能想问",
            "pricingFaq.q1": "社区版真的永久免费吗？有功能限制吗？", "pricingFaq.a1": "是的，社区版基于 AGPL-3.0 协议，永久免费，包含监控、告警、终端、剧本、SRE 中枢等所有核心功能，没有主机数量限制，也没有功能阉割。",
            "pricingFaq.q2": "专业版和企业版多了什么？", "pricingFaq.a2": "专业版提供工作日 SLA 响应与远程技术支持；企业版在此基础上增加专属技术顾问、定制开发、现场部署与培训服务，适合对稳定性与合规有更高要求的团队。",
            "pricingFaq.q3": "可以从社区版升级到企业版吗？数据会受影响吗？", "pricingFaq.a3": "可以无缝升级。所有版本使用同一套代码，升级只需替换二进制并激活授权，数据完全保留，无需迁移。",
            "pricingFaq.q4": "非营利组织或教育机构有优惠吗？", "pricingFaq.a4": "有。非营利组织、教育机构与开源贡献者可申请专业版 / 企业版特别折扣，请通过联系我们页面说明情况，我们会酌情处理。"
    },
    "zh-TW": {
      "page.title": "定價方案 — AIOps",
      "page.desc": "核心平台永久免費（AGPL-3.0 協議），企業可依需選購技術支援服務。一套程式碼，按需解鎖支援層級。"
      "page.oglocale": "zh_TW",
      "heroTag": "定價方案",
      "heroTitle": "開源免費，按需選擇支援",
      "heroDesc": "核心平台永久免費（AGPL-3.0 協議），企業可依需選購技術支援服務。一套程式碼，按需解鎖支援層級。"
      "recommendLabel": "推薦",
      "colCommunity": "社群版",
      "colStandard": "標準支援",
      "colEnterprise": "企業支援",
      "plans": [
        {
          "name": "社群版",
          "price": "¥0",
          "unit": "永久免費",
          "highlight": true,
          "desc": "完整核心功能，適合個人與中小團隊自建私有化監控。",
          "cta": "免費部署 →",
          "ctaHref": "https://github.com/sreyun/aiops-monitor",
          "features": [
            {
              "t": "全部監控 / 告警 / 終端 / 自動化能力",
              "ok": true
            },
            {
              "t": "PostgreSQL + VictoriaMetrics 雙引擎儲存",
              "ok": true
            },
            {
              "t": "Android / PWA 行動端",
              "ok": true
            },
            {
              "t": "社群支援（GitHub Issues）",
              "ok": true
            },
            {
              "t": "優先郵件回應（1–2 工作天）",
              "ok": false
            },
            {
              "t": "專屬解決方案架構師",
              "ok": false
            },
            {
              "t": "客製開發 / 私有化實施",
              "ok": false
            }
          ],
          "bestFor": "個人開發者 / 中小團隊 / 技術選型驗證"
        },
        {
          "name": "標準支援",
          "price": "按規模報價",
          "unit": "／年",
          "highlight": false,
          "desc": "為已上生產的團隊提供穩定保障。",
          "cta": "聯絡業務",
          "ctaHref": "contact.html",
          "features": [
            {
              "t": "包含社群版全部功能",
              "ok": true
            },
            {
              "t": "優先郵件回應（1–2 工作天）",
              "ok": true
            },
            {
              "t": "部署諮詢與最佳實踐",
              "ok": true
            },
            {
              "t": "社群支援",
              "ok": true
            },
            {
              "t": "專屬解決方案架構師",
              "ok": false
            },
            {
              "t": "客製開發 / 私有化實施",
              "ok": false
            }
          ],
          "bestFor": "已上線生產、需要穩定保障的成長型團隊"
        },
        {
          "name": "企業支援",
          "price": "客製報價",
          "unit": "／年",
          "highlight": false,
          "desc": "面向大型組織、合規與大規模落地場景。",
          "cta": "聯絡業務",
          "ctaHref": "contact.html",
          "features": [
            {
              "t": "包含標準支援全部能力",
              "ok": true
            },
            {
              "t": "專屬解決方案架構師",
              "ok": true
            },
            {
              "t": "私有化實施 + 客製開發",
              "ok": true
            },
            {
              "t": "培訓與合規落地支援",
              "ok": true
            },
            {
              "t": "大規模滾動升級保障",
              "ok": true
            }
          ],
          "bestFor": "大型企業 / 強合規 / 多機房大規模落地"
        }
      ],
      "compareTitle": "功能對比",
      "compareDesc": "同一套開源程式碼，按支援層級解鎖服務。",
      "matrix": {
        "groups": [
          {
            "group": "監控與告警",
            "rows": [
              {
                "label": "即時指標 / GPU / 撥測",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "多級閾值與降噪",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "遠端終端 + 會話回放",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "自動化劇本 / 自愈",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "AI 巡檢 / 根因診斷",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              }
            ]
          },
          {
            "group": "支援與服務",
            "rows": [
              {
                "label": "社群支援（GitHub）",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "優先郵件回應",
                "c": "—",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "部署諮詢 / 最佳實踐",
                "c": "—",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "專屬解決方案架構師",
                "c": "—",
                "s": "—",
                "e": "✓"
              },
              {
                "label": "客製開發 / 私有化實施",
                "c": "—",
                "s": "—",
                "e": "✓"
              },
              {
                "label": "培訓與合規落地",
                "c": "—",
                "s": "—",
                "e": "✓"
              }
            ]
          }
        ]
      },
      "ctaTitle": "準備好開始了嗎？",
      "ctaDesc": "社群版永久免費，一條命令私有化部署。",
      "ctaBtn1": "免費部署 →",
      "ctaBtn2": "查看功能",
      "footNote": "所有版本均基於同一套開源程式碼，資料永久自持、私有化部署，絕無功能降級或自動過期。",
            "whyFree.tag": "開源商業模式", "whyFree.title": "為什麼核心平台永久免費？", "whyFree.desc": "我們相信好的基礎設施軟體應該人人可用，商業化不靠功能閹割，而靠服務增值。",
            "whyFree.i1title": "AGPL-3.0 協議，程式碼全公開", "whyFree.i1desc": "所有核心功能 100% 開源，無隱藏模組、無功能閹割。你拿到的程式碼和社群完全一致。",
            "whyFree.i2title": "社群驅動，使用者即產品主人", "whyFree.i2desc": "功能優先級由社群投票與真實需求決定，不被資本裹挾，不做只為付費客戶定制的「企業特供版」。",
            "whyFree.i3title": "靠服務增值，不靠功能收費", "whyFree.i3desc": "專業版 / 企業版的價值在於技術支援、SLA 保障與定制諮詢，而非把免費功能鎖起來再賣一次。",
            "pricingFaq.tag": "定價 FAQ", "pricingFaq.title": "關於定價，你可能想問",
            "pricingFaq.q1": "社群版真的永久免費嗎？有功能限制嗎？", "pricingFaq.a1": "是的，社群版基於 AGPL-3.0 協議，永久免費，包含監控、告警、終端、劇本、SRE 中樞等所有核心功能，沒有主機數量限制，也沒有功能閹割。",
            "pricingFaq.q2": "專業版和企業版多了什麼？", "pricingFaq.a2": "專業版提供工作日 SLA 回應與遠端技術支援；企業版在此基礎上增加專屬技術顧問、定制開發、現場部署與培訓服務，適合對穩定性與合規有更高要求的團隊。",
            "pricingFaq.q3": "可以從社群版升級到企業版嗎？資料會受影響嗎？", "pricingFaq.a3": "可以無縫升級。所有版本使用同一套程式碼，升級只需替換二進制並啟動授權，資料完全保留，無需遷移。",
            "pricingFaq.q4": "非營利組織或教育機構有優惠嗎？", "pricingFaq.a4": "有。非營利組織、教育機構與開源貢獻者可申請專業版 / 企業版特別折扣，請透過聯絡我們頁面說明情況，我們會酌情處理。"
    },
    "en": {
      "page.title": "Pricing — AIOps",
      "page.desc": "The core platform is free forever under the AGPL-3.0 license. Enterprises can opt into paid technical support tiers. One codebase, support levels unlocked as you need them.",
      "page.oglocale": "en_US",
      "heroTag": "Pricing",
      "heroTitle": "Open Source, Free — Pay Only for Support",
      "heroDesc": "The core platform is free forever under the AGPL-3.0 license. Enterprises can opt into paid technical support tiers. One codebase, support levels unlocked as you need them.",
      "recommendLabel": "Popular",
      "colCommunity": "Community",
      "colStandard": "Standard",
      "colEnterprise": "Enterprise",
      "plans": [
        {
          "name": "Community",
          "price": "Free",
          "unit": "forever",
          "highlight": true,
          "desc": "Full core capabilities — ideal for individuals and small teams running self-hosted monitoring.",
          "cta": "Deploy Free →",
          "ctaHref": "https://github.com/sreyun/aiops-monitor",
          "features": [
            {
              "t": "All monitoring / alerting / terminal / automation",
              "ok": true
            },
            {
              "t": "PostgreSQL + VictoriaMetrics dual-engine storage",
              "ok": true
            },
            {
              "t": "Android / PWA mobile apps",
              "ok": true
            },
            {
              "t": "Community support (GitHub Issues)",
              "ok": true
            },
            {
              "t": "Priority email response (1–2 business days)",
              "ok": false
            },
            {
              "t": "Dedicated solutions architect",
              "ok": false
            },
            {
              "t": "Custom development / private implementation",
              "ok": false
            }
          ],
          "bestFor": "Individual developers / small teams / technical evaluation"
        },
        {
          "name": "Standard",
          "price": "Custom quote",
          "unit": "/ year",
          "highlight": false,
          "desc": "Stable assurance for teams already in production.",
          "cta": "Contact Sales",
          "ctaHref": "contact.html",
          "features": [
            {
              "t": "Everything in Community",
              "ok": true
            },
            {
              "t": "Priority email response (1–2 business days)",
              "ok": true
            },
            {
              "t": "Deployment consulting & best practices",
              "ok": true
            },
            {
              "t": "Community support",
              "ok": true
            },
            {
              "t": "Dedicated solutions architect",
              "ok": false
            },
            {
              "t": "Custom development / private implementation",
              "ok": false
            }
          ],
          "bestFor": "Growing teams in production that need reliable backing"
        },
        {
          "name": "Enterprise",
          "price": "Custom quote",
          "unit": "/ year",
          "highlight": false,
          "desc": "For large organizations, compliance, and large-scale rollouts.",
          "cta": "Contact Sales",
          "ctaHref": "contact.html",
          "features": [
            {
              "t": "Everything in Standard",
              "ok": true
            },
            {
              "t": "Dedicated solutions architect",
              "ok": true
            },
            {
              "t": "Private implementation + custom development",
              "ok": true
            },
            {
              "t": "Training & compliance onboarding",
              "ok": true
            },
            {
              "t": "Large-scale rolling-upgrade assurance",
              "ok": true
            }
          ],
          "bestFor": "Large enterprises / strict compliance / large-scale multi-DC rollout"
        }
      ],
      "compareTitle": "Feature Comparison",
      "compareDesc": "Same open-source codebase — support levels unlock services as you grow.",
      "matrix": {
        "groups": [
          {
            "group": "Monitoring & Alerting",
            "rows": [
              {
                "label": "Real-time metrics / GPU / probes",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "Multi-level thresholds & noise reduction",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "Remote terminal + session replay",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "Automation playbooks / self-healing",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "AI inspection / root-cause diagnosis",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              }
            ]
          },
          {
            "group": "Support & Services",
            "rows": [
              {
                "label": "Community support (GitHub)",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "Priority email response",
                "c": "—",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "Deployment consulting & best practices",
                "c": "—",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "Dedicated solutions architect",
                "c": "—",
                "s": "—",
                "e": "✓"
              },
              {
                "label": "Custom development / private implementation",
                "c": "—",
                "s": "—",
                "e": "✓"
              },
              {
                "label": "Training & compliance onboarding",
                "c": "—",
                "s": "—",
                "e": "✓"
              }
            ]
          }
        ]
      },
      "ctaTitle": "Ready to Get Started?",
      "ctaDesc": "Community is free forever — deploy privately with one command.",
      "ctaBtn1": "Deploy Free →",
      "ctaBtn2": "View Features",
      "footNote": "Every tier runs on the same open-source codebase — data stays permanently self-hosted, with no feature downgrade or automatic expiry.",
            "whyFree.tag": "Open Source Business Model", "whyFree.title": "Why is the core platform free forever?", "whyFree.desc": "We believe great infrastructure software should be available to everyone. We monetize through service, not feature lock-out.",
            "whyFree.i1title": "AGPL-3.0 License, fully transparent", "whyFree.i1desc": "All core features are 100% open source — no hidden modules, no feature gating. What you get is identical to the community.",
            "whyFree.i2title": "Community-driven, users in charge", "whyFree.i2desc": "Feature priorities are decided by community votes and real needs, not capital. No 'enterprise-only' editions.",
            "whyFree.i3title": "Revenue from service, not feature paywalls", "whyFree.i3desc": "Pro/Enterprise tiers add value through support, SLA, and consulting — not by locking free features behind a paywall.",
            "pricingFaq.tag": "Pricing FAQ", "pricingFaq.title": "Common Questions About Pricing",
            "pricingFaq.q1": "Is the Community Edition really free forever? Any feature limits?", "pricingFaq.a1": "Yes. The Community Edition is AGPL-3.0 licensed, free forever, with all core features including monitoring, alerts, terminal, playbooks, and SRE hub. No host limits, no feature gating.",
            "pricingFaq.q2": "What do Pro and Enterprise add?", "pricingFaq.a2": "Pro adds business-hour SLA response and remote support. Enterprise adds a dedicated technical advisor, custom development, on-site deployment, and training — for teams with higher stability and compliance needs.",
            "pricingFaq.q3": "Can I upgrade from Community to Enterprise? Will data be affected?", "pricingFaq.a3": "Seamless upgrade. All editions share the same codebase. Just replace the binary and activate the license — data stays intact, no migration needed.",
            "pricingFaq.q4": "Any discounts for non-profits or educational institutions?", "pricingFaq.a4": "Yes. Non-profits, educational institutions, and open-source contributors can apply for special discounts on Pro/Enterprise. Reach out via the Contact page with details."
    },
    "ja": {
      "page.title": "料金プラン — AIOps",
      "page.desc": "コアプラットフォームはAGPL-3.0ライセンスで永久無料。企業は必要に応じて有償の技術支援ティアを選択できます。一つのコードベース、必要な分だけ支援レベルを解放。",
      "page.oglocale": "ja_JP",
      "heroTag": "料金プラン",
      "heroTitle": "オープンソースは無料、支援は必要に応じて",
      "heroDesc": "コアプラットフォームはAGPL-3.0ライセンスで永久無料。企業は必要に応じて有償の技術支援ティアを選択できます。一つのコードベース、必要な分だけ支援レベルを解放。",
      "recommendLabel": "人気",
      "colCommunity": "コミュニティ版",
      "colStandard": "スタンダード",
      "colEnterprise": "エンタープライズ",
      "plans": [
        {
          "name": "コミュニティ版",
          "price": "無料",
          "unit": "永久",
          "highlight": true,
          "desc": "コア機能すべてを搭載。個人や中小チームの自ホスティング監視に最適。",
          "cta": "無料で導入 →",
          "ctaHref": "https://github.com/sreyun/aiops-monitor",
          "features": [
            {
              "t": "監視／アラート／ターミナル／自動化の全機能",
              "ok": true
            },
            {
              "t": "PostgreSQL + VictoriaMetrics 双引擎ストレージ",
              "ok": true
            },
            {
              "t": "Android / PWA モバイル",
              "ok": true
            },
            {
              "t": "コミュニティ支援（GitHub Issues）",
              "ok": true
            },
            {
              "t": "優先メール対応（1–2 営業日）",
              "ok": false
            },
            {
              "t": "専属ソリューションアーキテクト",
              "ok": false
            },
            {
              "t": "カスタム開発／プライベート実装",
              "ok": false
            }
          ],
          "bestFor": "個人開発者 / 中小チーム / 技術検証"
        },
        {
          "name": "スタンダード",
          "price": "規模別見積",
          "unit": "／年",
          "highlight": false,
          "desc": "本番稼働済みチームへ安定稼働の保障を。",
          "cta": "営業に問い合わせ",
          "ctaHref": "contact.html",
          "features": [
            {
              "t": "コミュニティ版の全機能を含む",
              "ok": true
            },
            {
              "t": "優先メール対応（1–2 営業日）",
              "ok": true
            },
            {
              "t": "導入コンサルティングとベストプラクティス",
              "ok": true
            },
            {
              "t": "コミュニティ支援",
              "ok": true
            },
            {
              "t": "専属ソリューションアーキテクト",
              "ok": false
            },
            {
              "t": "カスタム開発／プライベート実装",
              "ok": false
            }
          ],
          "bestFor": "本番稼働し、安定稼働を求める成長チーム"
        },
        {
          "name": "エンタープライズ",
          "price": "個別見積",
          "unit": "／年",
          "highlight": false,
          "desc": "大規模組織、コンプライアンス、大規模導入シナリオ向け。",
          "cta": "営業に問い合わせ",
          "ctaHref": "contact.html",
          "features": [
            {
              "t": "スタンダードの全能力を含む",
              "ok": true
            },
            {
              "t": "専属ソリューションアーキテクト",
              "ok": true
            },
            {
              "t": "プライベート実装 ＋ カスタム開発",
              "ok": true
            },
            {
              "t": "研修とコンプライアンス導入支援",
              "ok": true
            },
            {
              "t": "大規模ローリングアップグレード保障",
              "ok": true
            }
          ],
          "bestFor": "大企業 / 厳格なコンプライアンス / 大規模マルチDC展開"
        }
      ],
      "compareTitle": "機能比較",
      "compareDesc": "同じオープンソースコードベース —— 支援レベルに応じてサービスを解放。",
      "matrix": {
        "groups": [
          {
            "group": "監視とアラート",
            "rows": [
              {
                "label": "リアルタイムメトリクス / GPU / プローブ",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "多段階しきい値とノイズ削減",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "リモートターミナル ＋ セッション再生",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "自動化プレイブック／自己修復",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "AI巡検／根因診断",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              }
            ]
          },
          {
            "group": "支援とサービス",
            "rows": [
              {
                "label": "コミュニティ支援（GitHub）",
                "c": "✓",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "優先メール対応",
                "c": "—",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "導入コンサルティング／ベストプラクティス",
                "c": "—",
                "s": "✓",
                "e": "✓"
              },
              {
                "label": "専属ソリューションアーキテクト",
                "c": "—",
                "s": "—",
                "e": "✓"
              },
              {
                "label": "カスタム開発／プライベート実装",
                "c": "—",
                "s": "—",
                "e": "✓"
              },
              {
                "label": "研修とコンプライアンス導入",
                "c": "—",
                "s": "—",
                "e": "✓"
              }
            ]
          }
        ]
      },
      "ctaTitle": "始める準備はできましたか？",
      "ctaDesc": "コミュニティ版は永久無料、コマンド一つでプライベート導入。",
      "ctaBtn1": "無料で導入 →",
      "ctaBtn2": "機能を見る",
      "footNote": "すべてのティアは同一のオープンソースコードベースで、データは永久に自社保有・プライベート展開され、機能のダウングレードや自動期限切れは一切ありません。",
            "whyFree.tag": "オープンソースビジネスモデル", "whyFree.title": "なぜコアプラットフォームは永久無料なのか？", "whyFree.desc": "私たちは優れたインフラソフトウェアは誰もが使えるべきだと考えます。機能制限ではなくサービスで収益化します。",
            "whyFree.i1title": "AGPL-3.0 ライセンス、コードはすべて公開", "whyFree.i1desc": "全コア機能 100% オープンソース、非公開モジュールや機能制限なし。コミュニティと同一のコードです。",
            "whyFree.i2title": "コミュニティ主導、ユーザーが主人公", "whyFree.i2desc": "機能優先度はコミュニティ投票と実ニーズで決定。資本に左右されず、有料顧客専用版は作りません。",
            "whyFree.i3title": "サービスで付加価値、機能で課金しない", "whyFree.i3desc": "Pro/Enterprise の価値は技術サポート・SLA・コンサルティング。無料機能をロックして再販売しません。",
            "pricingFaq.tag": "料金 FAQ", "pricingFaq.title": "料金についてよくある質問",
            "pricingFaq.q1": "コミュニティ版は本当に永久無料ですか？機能制限は？", "pricingFaq.a1": "はい、コミュニティ版は AGPL-3.0 ライセンスで永久無料。監視・アラート・ターミナル・プレイブック・SRE ハブなど全コア機能を含み、ホスト数制限も機能制限もありません。",
            "pricingFaq.q2": "Pro と Enterprise には何が追加されますか？", "pricingFaq.a2": "Pro は営業日 SLA 対応とリモートサポートを追加。Enterprise には専任テクニカルアドバイザー・カスタム開発・オンサイト導入・トレーニングが含まれます。",
            "pricingFaq.q3": "コミュニティ版から Enterprise へアップグレードできますか？データは影響を受けますか？", "pricingFaq.a3": "シームレスにアップグレード可能。全エディションは同一コードベースで、バイナリを置き換えてライセンスを有効化するだけ。データはそのまま、移行不要です。",
            "pricingFaq.q4": "非営利団体や教育機関向けの割引はありますか？", "pricingFaq.a4": "はい。非営利団体・教育機関・オープンソース貢献者は Pro/Enterprise の特別割引を申請できます。お問い合わせページから状況をお知らせください。"
    }
  },
  "cases": {
    "zh-CN": {
      "page.title": "客户案例 — AIOps",
      "page.desc": "按行业归纳的典型落地范式 —— 看 AIOps 如何在你的领域创造价值。",
      "page.oglocale": "zh_CN",
      "heroTag": "客户案例",
      "heroTitle": "行业落地范式",
      "heroDesc": "按行业归纳的典型落地范式 —— 看 AIOps 如何在你的领域创造价值。",
      "note": "以下为按行业归纳的典型落地范式，关键指标为同类部署的代表性区间，非特定客户的承诺数据。",
      "items": [
        {
          "industry": "互联网 / 电商",
          "icon": "M3 12h4l3-9 4 18 3-9h4",
          "summary": "高并发Web与交易链路的全栈可观测，告警噪声压到最低，故障自愈让值班更安心。",
          "tags": [
            "高并发",
            "交易链路",
            "弹性扩缩"
          ],
          "results": [
            {
              "value": "99.9%",
              "label": "线上可用性"
            },
            {
              "value": "-80%",
              "label": "夜间无效告警"
            },
            {
              "value": "15 min",
              "label": "平均 MTTR"
            }
          ],
          "background": "大促与日常流量陡峰并存，交易链路牵一发动全身；监控对象横跨 Web、API、中间件与数据库，团队却只能在故障发生后被动救火。",
          "pain": "指标分散在多个开源组件，告警风暴频发，真正的交易异常被淹没；大促期间临时扩容后，新节点往往漏监控。",
          "solution": "AIOps 统一采集全栈指标与日志，按交易链路聚合视图；分级告警 + 自愈剧本自动隔离异常实例，值班只需关注真正影响用户的事件。"
        },
        {
          "industry": "金融 / 支付",
          "icon": "M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6",
          "summary": "对交易与清算链路的端到端监控，操作全程审计、权限精细隔离，贴合等保与合规审计要求。",
          "tags": [
            "等保合规",
            "交易链路",
            "审计追溯"
          ],
          "results": [
            {
              "value": "100%",
              "label": "操作可追溯"
            },
            {
              "value": "<5 min",
              "label": "故障定位"
            },
            {
              "value": "0",
              "label": "越权事件"
            }
          ],
          "background": "交易与清算对连续性要求极高，监管对操作可追溯、权限隔离与数据驻留有刚性合规要求。",
          "pain": "多套监控互不打通，故障定位跨系统扯皮；审计依赖人工导出，等保核查准备周期长、举证难。",
          "solution": "端到端监控交易/清算链路，操作全程审计录像 + 精细 RBAC；数据私有化自持，天然契合等保与合规审计，核查一键导出。"
        },
        {
          "industry": "制造 / 工业",
          "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z",
          "summary": "车间服务器与工控资产的统一巡检，硬件健康与网络流量一屏掌握，停产风险提前预警。",
          "tags": [
            "硬件巡检",
            "工控资产",
            "停产预警"
          ],
          "results": [
            {
              "value": "7×24",
              "label": "资产可观测"
            },
            {
              "value": "-60%",
              "label": "巡检工时"
            },
            {
              "value": "1000+",
              "label": "纳管主机"
            }
          ],
          "background": "车间服务器、工控 OT 资产与网络设备混杂，停产一分钟即意味着真金白银的损失。",
          "pain": "IT 与 OT 分属不同团队，资产健康各自为政；巡检靠人跑现场，故障往往在停机后才被发现。",
          "solution": "统一纳管 IT 与 OT 资产，硬件健康 + 网络流量一屏掌握；阈值预测提前预警停产风险，巡检工时下降六成。"
        },
        {
          "industry": "政企 / 医疗",
          "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z",
          "summary": "私有化自托管守住数据主权，终端录制与操作日志构成完整审计闭环，满足合规与信创要求。",
          "tags": [
            "数据自持",
            "信创合规",
            "审计闭环"
          ],
          "results": [
            {
              "value": "内网",
              "label": "数据不出网"
            },
            {
              "value": "全量",
              "label": "操作留痕"
            },
            {
              "value": "0",
              "label": "数据外泄"
            }
          ],
          "background": "数据主权与自主可控是底线，医疗/政务系统涉及大量敏感个人信息，必须留在内网。",
          "pain": "公有云 SaaS 监控无法过合规，自研脚本难维护；终端操作无留痕，出现越权或泄密难以追责。",
          "solution": "私有化自托管守住数据主权，终端录制 + 操作日志构成完整审计闭环；信创环境原生兼容，满足合规与本地化要求。"
        },
        {
          "industry": "游戏 / 音视频",
          "icon": "M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z",
          "summary": "开服与赛事高峰的弹性监控，远程终端秒级接入故障节点，AI 助手辅助根因定位。",
          "tags": [
            "弹性峰值",
            "低延迟",
            "实时排障"
          ],
          "results": [
            {
              "value": "秒级",
              "label": "故障接入"
            },
            {
              "value": "-75%",
              "label": "排障耗时"
            },
            {
              "value": "99.95%",
              "label": "服务可用"
            }
          ],
          "background": "开服、赛事与版本更新带来脉冲式流量，任何卡顿都直接影响玩家留存与收入。",
          "pain": "高峰期间节点抖动难以及时定位，跨机房排障靠人工 SSH 登录，定位慢、协作乱。",
          "solution": "弹性监控覆盖开服与赛事峰值，远程终端秒级接入故障节点；AI 助手辅助根因研判，排障耗时下降 75%。"
        }
      ],
      "testimonialsTitle": "他们怎么说",
      "ctaTitle": "你的场景，也能这样落地",
      "ctaDesc": "从单机到多机房，从合规到成本治理 —— 三分钟私有化部署，先把价值跑起来。",
      "ctaBtn1": "免费部署 →",
      "ctaBtn2": "联系我们",
      "secBackground": "行业背景",
      "secPain": "痛点分析",
      "secSolution": "解决方案"
    },
    "zh-TW": {
      "page.title": "客戶案例 — AIOps",
      "page.desc": "按行業歸納的典型落地範式 —— 看 AIOps 如何在你的領域創造價值。",
      "page.oglocale": "zh_TW",
      "heroTag": "客戶案例",
      "heroTitle": "行業落地範式",
      "heroDesc": "按行業歸納的典型落地範式 —— 看 AIOps 如何在你的領域創造價值。",
      "note": "以下為按行業歸納的典型落地範式，關鍵指標為同類部署的代表性區間，非特定客戶的承諾數據。",
      "items": [
        {
          "industry": "網際網路 / 電商",
          "icon": "M3 12h4l3-9 4 18 3-9h4",
          "summary": "高併發Web與交易鏈路的全棧可觀測，告警雜訊壓到最低，故障自愈讓值班更安心。",
          "tags": [
            "高併發",
            "交易鏈路",
            "彈性擴縮"
          ],
          "results": [
            {
              "value": "99.9%",
              "label": "線上可用性"
            },
            {
              "value": "-80%",
              "label": "夜間無效告警"
            },
            {
              "value": "15 min",
              "label": "平均 MTTR"
            }
          ],
          "background": "大促與日常流量陡峰並存，交易鏈路牽一髮動全身；監控對象橫跨 Web、API、中介軟體與資料庫，團隊卻只能在故障發生後被動救火。",
          "pain": "指標分散在多套開源元件，告警風暴頻發，真正的交易異常被淹沒；大促期間臨時擴容後，新節點往往漏監控。",
          "solution": "AIOps 統一採集全棧指標與日誌，依交易鏈路聚合視圖；分級告警 + 自愈劇本自動隔離異常實例，值班只需關注真正影響使用者的事件。"
        },
        {
          "industry": "金融 / 支付",
          "icon": "M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6",
          "summary": "對交易與清算鏈路的端到端監控，操作全程審計、權限精細隔離，貼合等保與合規審計要求。",
          "tags": [
            "等保合規",
            "交易鏈路",
            "審計追溯"
          ],
          "results": [
            {
              "value": "100%",
              "label": "操作可追溯"
            },
            {
              "value": "<5 min",
              "label": "故障定位"
            },
            {
              "value": "0",
              "label": "越權事件"
            }
          ],
          "background": "交易與清算對連續性要求極高，監管對操作可追溯、權限隔離與資料駐留有剛性合規要求。",
          "pain": "多套監控互不打通，故障定位跨系統扯皮；審計依賴人工匯出，等保核查準備週期長、舉證難。",
          "solution": "端到端監控交易/清算鏈路，操作全程審計錄影 + 精細 RBAC；資料私有化自持，天然契合等保與合規審計，核查一鍵匯出。"
        },
        {
          "industry": "製造 / 工業",
          "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z",
          "summary": "車間伺服器與工控資產的統一巡檢，硬體健康與網路流量一屏掌握，停產風險提前預警。",
          "tags": [
            "硬體巡檢",
            "工控資產",
            "停產預警"
          ],
          "results": [
            {
              "value": "7×24",
              "label": "資產可觀測"
            },
            {
              "value": "-60%",
              "label": "巡檢工時"
            },
            {
              "value": "1000+",
              "label": "納管主機"
            }
          ],
          "background": "車間伺服器、工控 OT 資產與網路設備混雜，停產一分鐘即意味著真金白銀的損失。",
          "pain": "IT 與 OT 分屬不同團隊，資產健康各自為政；巡檢靠人跑現場，故障往往在停機後才被發現。",
          "solution": "統一納管 IT 與 OT 資產，硬體健康 + 網路流量一屏掌握；閾值預測提前預警停產風險，巡檢工時下降六成。"
        },
        {
          "industry": "政企 / 醫療",
          "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z",
          "summary": "私有化自託管守住資料主權，終端錄製與操作日誌構成完整審計閉環，滿足合規與信創要求。",
          "tags": [
            "資料自持",
            "信創合規",
            "審計閉環"
          ],
          "results": [
            {
              "value": "內網",
              "label": "資料不出網"
            },
            {
              "value": "全量",
              "label": "操作留痕"
            },
            {
              "value": "0",
              "label": "資料外洩"
            }
          ],
          "background": "資料主權與自主可控是底線，醫療/政務系統涉及大量敏感個人資訊，必須留在內網。",
          "pain": "公有雲 SaaS 監控無法過合規，自研腳本難維護；終端操作無留痕，出現越權或洩密難以追責。",
          "solution": "私有化自託管守住資料主權，終端錄製 + 操作日誌構成完整審計閉環；信創環境原生相容，滿足合規與在地化要求。"
        },
        {
          "industry": "遊戲 / 影音",
          "icon": "M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z",
          "summary": "開服與賽事高峰的彈性監控，遠端終端秒級接入故障節點，AI 助手輔助根因定位。",
          "tags": [
            "彈性峰值",
            "低延遲",
            "即時排障"
          ],
          "results": [
            {
              "value": "秒級",
              "label": "故障接入"
            },
            {
              "value": "-75%",
              "label": "排障耗時"
            },
            {
              "value": "99.95%",
              "label": "服務可用"
            }
          ],
          "background": "開服、賽事與版本更新帶來脈衝式流量，任何卡頓都直接影響玩家留存與收入。",
          "pain": "高峰期間節點抖動難以及時定位，跨機房排障靠人工 SSH 登入，定位慢、協作亂。",
          "solution": "彈性監控覆蓋開服與賽事峰值，遠端終端秒級接入故障節點；AI 助手輔助根因研判，排障耗時下降 75%。"
        }
      ],
      "testimonialsTitle": "他們怎麼說",
      "ctaTitle": "你的場景，也能這樣落地",
      "ctaDesc": "從單機到多機房，從合規到成本治理 —— 三分鐘私有化部署，先把價值跑起來。",
      "ctaBtn1": "免費部署 →",
      "ctaBtn2": "聯絡我們",
      "secBackground": "行業背景",
      "secPain": "痛點分析",
      "secSolution": "解決方案"
    },
    "en": {
      "page.title": "Customers — AIOps",
      "page.desc": "Typical deployment patterns grouped by industry — see how AIOps creates value in your domain.",
      "page.oglocale": "en_US",
      "heroTag": "Customers",
      "heroTitle": "Industry Playbooks",
      "heroDesc": "Typical deployment patterns grouped by industry — see how AIOps creates value in your domain.",
      "note": "The following are typical deployment patterns grouped by industry. Key metrics are representative ranges for similar deployments, not commitments for any specific customer.",
      "items": [
        {
          "industry": "Internet / E-commerce",
          "icon": "M3 12h4l3-9 4 18 3-9h4",
          "summary": "Full-stack observability for high-concurrency web and transaction paths, with alert noise minimized and self-healing keeping on-call calm.",
          "tags": [
            "High concurrency",
            "Transaction paths",
            "Elastic scaling"
          ],
          "results": [
            {
              "value": "99.9%",
              "label": "Online availability"
            },
            {
              "value": "-80%",
              "label": "False-night alerts"
            },
            {
              "value": "15 min",
              "label": "Avg MTTR"
            }
          ],
          "background": "Traffic swings between flash sales and daily peaks, and the transaction path is all-or-nothing; monitoring sprawls across web, API, middleware and databases, yet teams still fight fires only after incidents occur.",
          "pain": "Metrics are scattered across multiple open-source components, alert storms are constant, and real transaction anomalies get buried; newly scaled nodes during promotions are often left unmonitored.",
          "solution": "AIOps unifies full-stack metrics and logs with a transaction-path aggregated view; tiered alerting and self-healing playbooks auto-isolate anomalous instances, so on-call only handles events that truly impact users."
        },
        {
          "industry": "Finance / Payments",
          "icon": "M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6",
          "summary": "End-to-end monitoring of transaction and settlement paths, with full operation audit and fine-grained access isolation, aligned to compliance and audit requirements.",
          "tags": [
            "Compliance",
            "Transaction paths",
            "Audit trail"
          ],
          "results": [
            {
              "value": "100%",
              "label": "Ops traceable"
            },
            {
              "value": "<5 min",
              "label": "Fault locating"
            },
            {
              "value": "0",
              "label": "Privilege abuses"
            }
          ],
          "background": "Trading and settlement demand extreme continuity, while regulators impose hard compliance requirements on operation traceability, access isolation and data residency.",
          "pain": "Multiple monitoring stacks don't talk to each other, so fault localization turns into cross-team blame; audits rely on manual exports, making compliance checks slow and hard to evidence.",
          "solution": "End-to-end monitoring of transaction and settlement paths, with full session recording and fine-grained RBAC; data stays private and self-hosted, naturally fitting compliance audits with one-click evidence export."
        },
        {
          "industry": "Manufacturing / Industrial",
          "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z",
          "summary": "Unified inspection of shop-floor servers and OT assets, with hardware health and network traffic on one screen and downtime risks warned early.",
          "tags": [
            "Hardware inspection",
            "OT assets",
            "Downtime warning"
          ],
          "results": [
            {
              "value": "7×24",
              "label": "Asset visibility"
            },
            {
              "value": "-60%",
              "label": "Inspection hours"
            },
            {
              "value": "1000+",
              "label": "Hosts managed"
            }
          ],
          "background": "Shop-floor servers, OT assets and network gear are mixed together, where one minute of downtime means real money lost.",
          "pain": "IT and OT sit in separate teams, each with its own asset health view; inspections rely on people walking the floor, so failures are often found only after a stoppage.",
          "solution": "Unified management of IT and OT assets puts hardware health and network traffic on one screen; threshold prediction warns of downtime risk early, cutting inspection hours by 60%."
        },
        {
          "industry": "Government / Healthcare",
          "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z",
          "summary": "Private self-hosting keeps data sovereignty; terminal recording and operation logs form a complete audit loop, meeting compliance and local-tech requirements.",
          "tags": [
            "Data sovereignty",
            "Local-tech compliance",
            "Audit loop"
          ],
          "results": [
            {
              "value": "Intranet",
              "label": "Data stays in"
            },
            {
              "value": "Full",
              "label": "Ops logged"
            },
            {
              "value": "0",
              "label": "Data leaks"
            }
          ],
          "background": "Data sovereignty and local-tech autonomy are non-negotiable; medical and government systems hold vast sensitive personal data that must stay on the intranet.",
          "pain": "Public-cloud SaaS monitoring can't pass compliance, and home-grown scripts are hard to maintain; terminal operations leave no trail, so privilege abuse or leaks are hard to attribute.",
          "solution": "Private self-hosting keeps data sovereignty; terminal recording and operation logs form a complete audit loop; native compatibility with local-tech (Xinchuang) environments meets compliance and localization needs."
        },
        {
          "industry": "Gaming / Streaming",
          "icon": "M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z",
          "summary": "Elastic monitoring for launch and event peaks, with remote terminal connecting to failed nodes in seconds and the AI assistant aiding root-cause locating.",
          "tags": [
            "Elastic peaks",
            "Low latency",
            "Live troubleshooting"
          ],
          "results": [
            {
              "value": "Seconds",
              "label": "Fault access"
            },
            {
              "value": "-75%",
              "label": "Troubleshoot time"
            },
            {
              "value": "99.95%",
              "label": "Service uptime"
            }
          ],
          "background": "Launches, tournaments and version updates bring pulse-like traffic, where any hiccup directly hits player retention and revenue.",
          "pain": "Node jitter during peaks is hard to locate in time; cross-DC troubleshooting relies on manual SSH, making localization slow and collaboration messy.",
          "solution": "Elastic monitoring covers launch and event peaks with remote terminal connecting to failed nodes in seconds; the AI assistant aids root-cause analysis, cutting troubleshooting time by 75%."
        }
      ],
      "testimonialsTitle": "What They Say",
      "ctaTitle": "Your Scenario Can Land Like This Too",
      "ctaDesc": "From single host to multi-DC, from compliance to cost governance — deploy privately in 3 minutes and see the value first.",
      "ctaBtn1": "Deploy Free →",
      "ctaBtn2": "Contact Us",
      "secBackground": "Industry Background",
      "secPain": "Pain Points",
      "secSolution": "Solution"
    },
    "ja": {
      "page.title": "導入事例 — AIOps",
      "page.desc": "業界ごとに整理した代表的な導入パターン —— AIOps があなたの領域でどう価値を生むか。",
      "page.oglocale": "ja_JP",
      "heroTag": "導入事例",
      "heroTitle": "真の業界、真の成果",
      "heroDesc": "業界ごとに整理した代表的な導入パターン —— AIOps があなたの領域でどう価値を生むか。",
      "note": "以下は業界ごとに整理した代表的な導入パターンです。主要指標は類似導入の代表的な範囲であり、特定のお客様へのコミットメント数値ではありません。",
      "items": [
        {
          "industry": "インターネット / EC",
          "icon": "M3 12h4l3-9 4 18 3-9h4",
          "summary": "高同時接続Webと取引経路の全スタック可観測。アラートノイズを最小限に抑え、自己修復で当番を安心に。",
          "tags": [
            "高同時接続",
            "取引経路",
            "弾性スケーリング"
          ],
          "results": [
            {
              "value": "99.9%",
              "label": "オンライン可用率"
            },
            {
              "value": "-80%",
              "label": "夜間無効アラート"
            },
            {
              "value": "15 min",
              "label": "平均MTTR"
            }
          ],
          "background": "大規模セールと日常のトラフィック急増が混在し、取引経路は一処障害で全体に波及します。監視対象は Web・API・ミドルウェア・DB にまたがり、チームは事故発生後にしか対応できません。",
          "pain": "指標が複数の OSS コンポーネントに散らばり、アラートストームが絶えず、本当の取引異常は埋もれます。セール時の臨時増強ノードは監視から漏れがちです。",
          "solution": "AIOps はフルスタックの指標とログを統合し、取引経路ごとの集約ビューを提供します。階層型アラートと自己修復プレイブックが異常インスタンスを自動隔離し、当番はユーザーに影響する事象だけに対応すれば済みます。"
        },
        {
          "industry": "金融 / 決済",
          "icon": "M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6",
          "summary": "取引・決済経路のエンドツーエンド監視。操作の全記録監査ときめ細かな権限分離で、等級保護（等保）とコンプライアンス監査要件に合致。",
          "tags": [
            "等保コンプライアンス",
            "取引経路",
            "監査追跡"
          ],
          "results": [
            {
              "value": "100%",
              "label": "操作追跡可能"
            },
            {
              "value": "5分未満",
              "label": "障害特定"
            },
            {
              "value": "0",
              "label": "権限越え事件"
            }
          ],
          "background": "取引と決済は極めて高い継続性を求められ、監査側は操作の追跡可能性・アクセス分離・データの域内保持に厳格なコンプライアンスを求めます。",
          "pain": "複数の監視スタックが連携せず、障害切り分けは部門間の責任の押し付け合いに。監査は手動エクスポートに頼り、コンプライアンス確認は遅く証拠化も困難です。",
          "solution": "取引・決済経路のエンドツーエンド監視に、全セッション録画ときめ細かな RBAC を組み合わせます。データはプライベート自ホスティングで完全に自社保有となり、コンプライアンス監査に自然に合致し、証拠はワンクリックで出力できます。"
        },
        {
          "industry": "製造 / 工業",
          "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z",
          "summary": "工場サーバーとOT資産の統一巡検。ハードウェア健康とネットワークトラフィックを1画面で掌握し、停止リスクを事前警告。",
          "tags": [
            "ハードウェア巡検",
            "OT資産",
            "停止予警"
          ],
          "results": [
            {
              "value": "7×24",
              "label": "資産可視化"
            },
            {
              "value": "-60%",
              "label": "巡検工数"
            },
            {
              "value": "1000+",
              "label": "管理ホスト"
            }
          ],
          "background": "工場サーバー・OT 資産・ネットワーク機器が混在し、1 分の停止がそのまま金銭的損失になります。",
          "pain": "IT と OT が別チームで、資産の健康状態は各々に分断されています。巡検は現場への人力依存で、停止してからでないと故障に気づけません。",
          "solution": "IT と OT 資産を統一管理し、ハードウェア健康とネットワークトラフィックを 1 画面に。閾値予測で停止リスクを事前警告し、巡検工数を 6 割削減します。"
        },
        {
          "industry": "官公庁 / 医療",
          "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z",
          "summary": "プライベート自ホスティングでデータ主権を確保。端末録画と操作ログが完全な監査ループを構成し、コンプライアンスと国産技術（信創）要件を満たす。",
          "tags": [
            "データ自社保有",
            "信創コンプライアンス",
            "監査ループ"
          ],
          "results": [
            {
              "value": "内網",
              "label": "データ不出域"
            },
            {
              "value": "全量",
              "label": "操作記録"
            },
            {
              "value": "0",
              "label": "データ漏洩"
            }
          ],
          "background": "データ主権と国産技術（信創）への対応は譲れません。医療・官公庁システムは大量の機微な個人情報を扱い、すべて内網に留める必要があります。",
          "pain": "パブリッククラウドの SaaS 監視ではコンプライアンスを通せず、自作スクリプトは保守が困難です。端末操作に履歴が残らず、権限越えや漏洩の責任特定が困難です。",
          "solution": "プライベート自ホスティングでデータ主権を確保。端末録画と操作ログが完全な監査ループを構成し、信創環境にネイティブ対応でコンプライアンスとローカライゼーション要件を満たします。"
        },
        {
          "industry": "ゲーム / 映像",
          "icon": "M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z",
          "summary": "サービス開始とイベントピークの弾性監視。リモートターミナルが障害ノードに秒単位で接続し、AIアシスタントが根因特定を支援。",
          "tags": [
            "弾性ピーク",
            "低遅延",
            "即時排障"
          ],
          "results": [
            {
              "value": "秒級",
              "label": "障害接続"
            },
            {
              "value": "-75%",
              "label": "排障工数"
            },
            {
              "value": "99.95%",
              "label": "サービス可用"
            }
          ],
          "background": "サービス開始・イベント・バージョン更新は脈動的なトラフィックをもたらし、わずかなカクつきがプレイヤーの定着と収益に直結します。",
          "pain": "ピーク時のノード揺らぎは即座に特定できず、跨 DC の排障は手動 SSH に頼るため特定が遅く協業も乱雑です。",
          "solution": "サービス開始とイベントのピークを弾性監視し、リモートターミナルが障害ノードに秒単位で接続。AI アシスタントが根因特定を支援し、排障工数を 75% 削減します。"
        }
      ],
      "testimonialsTitle": "お客様の声",
      "ctaTitle": "あなたのシナリオも、こう実現できます",
      "ctaDesc": "単一ホストからマルチDC、コンプライアンスからコストガバナンスまで —— 3分でプライベート導入、まずは価値を動かして。",
      "ctaBtn1": "無料で導入 →",
      "ctaBtn2": "お問い合わせ",
      "secBackground": "業界背景",
      "secPain": "課題分析",
      "secSolution": "解決策"
    }
  },
  "index": {
    "ja": {
      "page.title": "AIOps — オープンソースの可観測性・SRE プラットフォーム。Zabbix + Prometheus + Grafana を単一バイナリで置換",
      "page.desc": "Zabbix + Prometheus + Grafana + Alertmanager + 自動化 Playbook + 踏み台サーバーを単一バイナリで置換。AIOps は 100% オープンソース、プライベート自ホスティングのエンタープライズ級可観測性・SRE プラットフォームです。リアルタイム監視、インテリジェントなアラート、リモートターミナル/デスクトップ、自動自己修復、AI 巡検診断、MCP 連携、SRE クローズドループ、Android / HarmonyOS モバイルコンソールを備え、コマンド1つで導入、データは永久に自社保有。",
      "page.oglocale": "ja_JP",
      "hero.badge": "100% オープンソース · データ自社保有 · 3分で導入完了",
      "hero.title": "運用チームを<br><span class=\"gradient-text\">アラート疲れと深夜の消火活動</span>から解放",
      "hero.desc": "AIOps は単一の Go バイナリと依存ゼロの Agent で、収集・アラート・リモートターミナル/デスクトップ・自動自己修復から、SRE クローズドループ、AI 巡検/MCP、Android / HarmonyOS モバイルコンソールまでの運用全行程をカバーします。PostgreSQL + VictoriaMetrics の二重ストレージでデータは完全に自社保有。コマンド1つで導入、3分で本稼働。",
      "hero.creds": "初回ログインでユーザー名＋パスワードの変更を必須化。管理者アカウントへの MFA 有効化を推奨",
      "hero.proof": "100% オープンソース · コマンド1つで導入 · データ永久自社保有",
      "hero.positioning": "プライベートデプロイ、アラートから自癒まで一条閉環 —— 深夜の消火活動を定時退勤に変えます。",
      "hero.quickStart": "5分でクイックスタート",
      "hero.docs": "ドキュメント",
      "hero.changelog": "更新履歴",
      "hero.titleNew": "1つのバイナリで<br><span class=\"gradient-text\">Zabbix + Prometheus + Grafana</span> を置換",
      "hero.stat1.num": "5000+",
      "hero.stat1.label": "安定稼働ホスト",
      "hero.stat2.num": "3",
      "hero.stat2.label": "導入完了",
      "hero.stat2.unit": "分",
      "hero.stat3.num": "0",
      "hero.stat3.label": "手動設定",
      "hero.stat4.num": "100%",
      "hero.stat4.label": "オープンソース無料",
      "pain.tag": "これらの状況、きっと見覚えがあります",
      "pain.title": "中小チームの運用から逃れられない4つの難題",
      "pain.desc": "人手不足、ツールの分散、アラートの殺到、人手による切り分け —— これらの問題がチームの効率と睡眠を静かに奪っています",
      "pain1.title": "人手が著しく不足",
      "pain1.desc": "1〜2人の運用で数十から数百台のマシンを管理。日常の巡検、障害対応、セキュリティパッチはすべて人手。残業が当たり前。",
      "pain1.sol": "コマンド1つで Agent を自動導入・一括管理。運用人力を最大70%削減（社内実測）",
      "pain2.title": "アラート疲れの殺到",
      "pain2.desc": "每台のマシンに多数の監視項目があり、アラートが押し寄せても優先順位がつかず、本当に重大な障害がノイズに埋もれる。",
      "pain2.sol": "階層別アラート（重大/警告）＋ 重複排除・冷却 ＋ デスクトップ通知。本当に処理が必要なものだけを通知",
      "pain3.title": "障害切り分けに時間がかかる",
      "pain3.desc": "問題が起きるとまず SSH でログインしてコマンドを叩き、特定は経験頼み。複数人で協力しても誰が何をしたか記録がない。",
      "pain3.sol": "リモートターミナル（ポート開放不要）＋ セッション再生 ＋ 操作監査で、障害を5分で特定",
      "pain4.title": "監視ツールの断片化",
      "pain4.desc": "Prometheus でメトリクス、Grafana で可視化、Alertmanager でアラート、Jira でチケット —— 5、6個のツールをつなぎ合わせ、導入と保守コストは高止まり。",
      "pain4.sol": "単一バイナリで監視＋アラート＋ターミナル＋自動化を実現し、Zabbix + Prometheus + Grafana + Alertmanager を置換",
      "feat.tag": "コア機能",
      "feat.title": "1つのプラットフォームで、監視から SRE までの全行程をカバー",
      "feat.desc": "収集、アラート、ターミナル/デスクトップ、Playbook から、SRE ハブ、ログ検索、AI 巡検、MCP ツール連携まで —— 単一バイナリのクローズドループ。複数ツールをつなぎ合わせる必要も、監視スタック専任チームを養う必要もありません。",
      "feat1.title": "リアルタイム監視とトレンド",
      "feat1.desc": "CPU / メモリ / SWAP / 複数ディスク / ネットワーク / TCP / 負荷 / プロセス / GPU —— 5秒間隔で収集、インタラクティブなトレンドグラフ、時系列は VictoriaMetrics に統一保存。",
      "feat1.val": "台ごとに SSH で状態を見る必要なし、1か所で全体を把握",
      "feat2.title": "マルチクラウド・インテリジェントアラートとメッセージセンター",
      "feat2.desc": "27 次元のしきい値カスタマイズ ＋ 階層別・重複排除・冷却。Feishu/DingTalk/メール ＋ アリクラウド/ファーウェイクラウド/テンセントクラウドの多クラウド SMS と音声通話でマルチチャネル通知。サイト内メッセージセンターがイベント / AI 診断 / 自動修復 / チケットを集約し、ワンクリックで直接遷移。",
      "feat2.val": "重大アラートは電話で人を叩き起こし、対応漏れなし",
      "feat3.title": "SRE ハブ",
      "feat3.desc": "インシデントクローズドループ（アラート / SLO / 手動集約 ＋ タイムライン）・アラート→Playbook 自動修復（ガードレール ＋ 承認）・SLO / エラーバジェット・チケットフロー。",
      "feat3.val": "アラートから修復までワンストップのクローズドループ",
      "feat3.visualTitle": "アラート → 診断 → 自癒 → 振り返り",
      "feat3.visualDesc": "全流程自動化閉環、人手による組み立て不要",
      "feat4.title": "ログ収集 ＋ AI 巡検診断",
      "feat4.desc": "Agent が増分ログ収集 ＋ 全文検索。AI 定期巡検 ＋ 根本原因研判。MCP Streamable HTTP で当番/診断ツールを Cursor / Claude に接続可能。音声設定はワンクリックで TTS/STT 自己テスト。",
      "feat4.val": "根本原因の特定を、人手の経験からインテリジェントな研判へ",
      "feat5.title": "リモートターミナルと自動化 Playbook",
      "feat5.desc": "ブラウザ内の完全 TTY（ポート開放不要）＋ ターミナル二次パスワード ＋ 録画再生 ＋ 読み取り専用傍観。Playbook をビジュアルに編成し、複数ホストへ一括並行配信。",
      "feat5.val": "障害切り分けを30分から5分に短縮",
      "feat6.title": "エンタープライズ級ストレージとセキュリティ",
      "feat6.desc": "PostgreSQL + VictoriaMetrics で統一ストレージ。RBAC + MFA + 設定キーの AES-256-GCM 静止暗号化 ＋ オプションの TLS 転送暗号化。4プラットフォームのネイティブ収集（麒麟含む）、AMD64 + ARM64 を全面カバー。",
      "feat6.val": "データ自社保有 ＋ 静止暗号化で、等級保護（等保）監査に合致",
      "arch.tag": "動作原理",
      "arch.title": "1つのアーキテクチャで、収集から運用までのクローズドループをカバー",
      "arch.desc": "Agent がリバース接続でポート開放不要。データは単一バイナリのサーバーに集約され、アラート / ターミナル / Playbook をワンストップで完了",
      "arch.linux": "Linux Agent",
      "arch.linuxSub": "/proc + syscall ネイティブ収集",
      "arch.win": "Windows Agent",
      "arch.winSub": "Win32 API + ConPTY ターミナル",
      "arch.mac": "macOS Agent",
      "arch.macSub": "sysctl + Apple GPU",
      "arch.serverTitle": "AIOps サーバー",
      "arch.serverSub": "単一バイナリ · PG + VM 統一ストレージ",
      "arch.cap1": "メトリクス + ログ",
      "arch.cap2": "アラート + メッセージセンター",
      "arch.cap3": "SRE ハブ + 自動修復",
      "arch.cap4": "AI 巡検診断",
      "arch.cap5": "ターミナル / Playbook / RBAC",
      "arch.panel": "ブラウザ内リアルタイムパネル ＋ Android / HarmonyOS",
      "arch.panelSub": "PWA · マルチ端末アクセス · ネイティブモバイルコンソール",
      "arch.notify": "Feishu / DingTalk / メール / SMS / 音声通話",
      "arch.notifySub": "階層別アラート通知",
      "arch.multi": "マルチサーバー / リレー",
      "arch.multiSub": "マルチDC 災害復旧 · クロスセグメント透過",
      "arch.hw": "ハードウェア巡検 / NetFlow / OceanStor",
      "arch.hwSub": "Redfish · トラフィック分析 · ストレージ収集",
      "cta.title": "3分で、運用を軽く",
      "cta.demo": "デモを予約",
      "cta.desc": "docker-compose.yml をダウンロード。コマンド1つでキーを自動生成して書き込み、docker compose でワンクリック起動。手動設定不要、追加の DB ミドルウェアも不要。",
      "cta.cmd": "# GitHub からダウンロード<br>bash <(curl -fsSL https://raw.githubusercontent.com/sreyun/aiops-monitor/master/scripts/secure-compose.sh)<br><br># Gitee ミラーからダウンロード（GitHub にアクセスしにくい場合に推奨）<br>bash <(curl -fsSL https://gitee.com/bigdatasafe/aiops-monitor/raw/master/scripts/secure-compose.sh)<br><br># 起動（キーは書き込み済み、手動変更不要）<br>docker compose up -d<br><span style='color:var(--muted)'># ブラウザで http://localhost:8529 を開く</span>",
      "cta.btn2": "機能の詳細を見る",
      "trust.tag": "技術エコシステム",
      "trust.title": "既存の技術スタックに完璧に溶け込む",
      "trust.desc": "主要 OS、コンテナ化デプロイ、チーム協業ツールをネイティブサポート。導入即稼働、インフラの改修は不要",
      "proof.usage": "1〜5000+ 台のホストを安定稼働",
      "proof.usageSub": "個人のプロジェクトから中規模企業まで、1つのプラットフォームでスムーズに拡張",
      "proof.item2num": "PG + VM",
      "proof.item2label": "二重エンジン統一ストレージ",
      "proof.item3num": "データ自社保有",
      "proof.item3label": "社内プライベート · 永久保存",
      "proof.item4num": "27 次元",
      "proof.item4label": "インテリジェントしきい値 · 階層別アラート",
      "proof.q1": "「以前は Zabbix、Prometheus、Grafana がそれぞれ別管理で、重大アラートがいつも深夜に漏れていた。AIOps に替えてから、しきい値、自己修復、チケットがすべて1つのクローズドループに繋がり、アラートにやっと担当とその後の処置がついた。」",
      "proof.q2": "「リモートターミナル ＋ セッション再生 ＋ 操作監査のおかげで、障害特定が30分から5分に短縮。深夜に呼び出される回数が目に見えて減った。」",
      "proof.q3": "「スタートアップだから運人は2人だけ。AIOps 導入後、しきい値も自己修復もチケットも全部1つの閉環になって、やっとアラートに担当と後続対応が紐づいた。」",
      "proof.q3by": "某 SaaS スタートアップ · CTO · 約30台のホスト",
      "proof.q4": "「跨省の複数データセンターにまたがるホストを管理。AIOps の一括管理で、もはや各拠点に専任を置く必要なし。」",
      "proof.q4by": "某製造業 IT · 運用主管 · 跨省3データセンター",
      "integrations": [
        {
          "name": "Linux",
          "icon": "M9 3v2M15 3v2M5 7h14a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V9a2 2 0 0 1 2-2z"
        },
        {
          "name": "Windows",
          "icon": "M3 5h8v6H3zM13 5h8v6h-8zM3 13h8v6H3zM13 13h8v6h-8z"
        },
        {
          "name": "macOS",
          "icon": "M12 2a10 10 0 1 0 10 10A10 10 0 0 0 12 2zM2 12h20M12 2a15 15 0 0 1 0 20 15 15 0 0 1 0-20z"
        },
        {
          "name": "Docker",
          "icon": "M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"
        },
        {
          "name": "Python",
          "icon": "M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4"
        },
        {
          "name": "Feishu",
          "icon": "M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z"
        },
        {
          "name": "DingTalk",
          "icon": "M8 12a4 4 0 1 0 8 0 4 4 0 0 0-8 0zM3 12h2M19 12h2M12 3v2M12 19v2"
        },
        {
          "name": "SMTP メール",
          "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6"
        }
      ],
      "fwd.tag": "独自機能",
      "fwd.title": "ブラウザ内ポート転送。インネットワークサービスを安全に公開",
      "fwd.desc": "公開ネットワークにポートを開くことなく、Agent のリバーストンネルを通じてインネットワークの Web / データベース / デバッグインターフェースをローカルのブラウザに安全にマッピング。TCP / UDP / HTTP の3プロトコルの単一ポート転送、および TCP / UDP のポート範囲一括転送に対応。リストとカードの2ビューで、有効 / 無効 / コピー / 編集 / 削除をワンクリックで完了。",
      "fwd.term1": "# TCP 単一ポート：インネットワーク DB をローカルにマッピング",
      "fwd.term2": "# UDP ＋ ポート範囲：連続ポートを一度にマッピング",
      "fwd.term3": "✓ 11 ルール · 同グループ一括起動停止 · Agent トンネル経由",
      "fwd.points": [
        "TCP / UDP / HTTP の3プロトコル単一ポート転送。データベース、Web 管理画面、DNS / ゲーム / 映像音声などに対応",
        "TCP / UDP ポート範囲一括転送。連続ポートを一度にマッピング（1バッチ最大100ポート）、グループ単位で一括起動停止・削除",
        "Agent リバース接続で、インネットワークサービスは公開ポートゼロ",
        "リスト / カードの2ビューで、転送状態が一目瞭然",
        "有効 / 無効 / コピー / 編集 / 削除で、運用操作がクローズドループ",
        "転送統計とヘルスチェックで、異常をいち早く検知"
      ],
      "fwd.cta": "すべての機能を見る",
      "android.tag": "モバイル運用",
      "android.title": "運用センターをポケットに",
      "android.desc": "ネイティブ Android / HarmonyOS コンソールなら、いつでもホスト状態とアラートを把握し、リモートターミナルで排障可能。インストールパッケージは別配布（モバイルソースは本リポジトリに含まれません）。",
      "android.p1": "リアルタイムホスト総覧：CPU / メモリ / ディスク / ネットワークが一目で、複数ホストをスムーズに切り替え",
      "android.p2": "アラート即時通知：重大アラートはスマホにポップアップ。タップで詳細と対処提案を表示",
      "android.p3": "いつでもリモートターミナル：内蔵の安全ターミナルで、スマホからも SSH 排障。セッションは再生・監査可能",
      "android.p4": "ネイティブで快適な体験：Kotlin + Jetpack Compose 製。WebView の外枠なし",
      "android.p5": "プライベート自ホスティング：サーバーアドレスをカスタマイズ。データは常にあなたの社内ネットワークに",
      "android.badge1": "Android / HarmonyOS",
      "android.badge2": "プライベートデプロイ",
      "android.cta": "モバイル機能を見る →",
      "android.m1l": "CPU 負荷",
      "android.m2l": "メモリ使用",
      "android.m3l": "オンラインホスト",
      "android.m1v": "23% 正常",
      "android.m2v": "61%",
      "android.m3v": "12 台",
      "android.m4": "ターミナル接続済み · db-prod-01",
      "faq.tag": "よくある質問",
      "faq.title": "AIOps について、知りたいことは",
      "faq.desc": "導入、セキュリティ、性能、拡張 —— 最もよくある疑問をまとめました",
      "faq.viewAll": "すべてのよくある質問を見る →"
    }
  },
  "comparison": {
    "ja": {
      "page.title": "製品比較 — AIOps",
      "page.desc": "Zabbix / Prometheus+Grafana / 商用 APM と総合比較：統合 SRE プラットフォーム、プライベート化によるデータ自主、業界標準 PG+VM ストレージによる導入即拡張という差別化の優位性。",
      "page.oglocale": "ja_JP",
      "head.tag": "製品比較",
      "head.title": "なぜ AIOps を選ぶのか？",
      "head.desc": "1つのプラットフォームで 監視→アラート→ログ→ターミナル→Playbook→SRE→AI をカバー。データはあなたの社内ネットワークに残り、業界標準の PostgreSQL ＋ VictoriaMetrics が担うため、数台から万単位へスムーズに拡張",
      "adv.tag": "コアの優位性",
      "adv.title": "中小企業が AIOps を選ぶ3つの理由",
      "cta.title": "監視ツールの導入と保守にお金を払うのは止めよう",
      "cta.desc": "浮いた時間と予算を、本当に事業価値を生むことに使ってください",
      "cta.btn1": "無料で導入 →",
      "cta.btn2": "ソリューションを見る",
      "table": {
        "headers": [
          "能力の観点",
          "AIOps",
          "Zabbix",
          "Prometheus + Grafana",
          "商用 APM"
        ],
        "rows": [
          [
            [
              "導入方式",
              ""
            ],
            [
              "単一バイナリ ＋ コマンド1つ",
              "yes"
            ],
            [
              "Server ＋ DB ＋ Agent の複数コンポーネント",
              "no"
            ],
            [
              "Prometheus ＋ Grafana ＋ AlertManager",
              "no"
            ],
            [
              "Agent ＋ SaaS アカウント",
              "no"
            ]
          ],
          [
            [
              "時系列ストレージ",
              ""
            ],
            [
              "VictoriaMetrics（業界標準 · 高圧縮 · 万単位に対応）",
              "yes"
            ],
            [
              "MySQL / PG（大規模書き込みに弱い）",
              ""
            ],
            [
              "ローカル TSDB（単機の保持に限界）",
              ""
            ],
            [
              "クラウドホスティング（データはクラウドへ）",
              "no"
            ]
          ],
          [
            [
              "導入時間",
              ""
            ],
            [
              "3 分",
              "yes"
            ],
            [
              "30〜60 分",
              "no"
            ],
            [
              "1〜2 時間（設定込み）",
              "no"
            ],
            [
              "10〜30 分",
              "no"
            ]
          ],
          [
            [
              "学習曲線",
              ""
            ],
            [
              "低（導入即稼働）",
              "yes"
            ],
            [
              "中〜高（テンプレート/トリガー/Low-level discovery）",
              "no"
            ],
            [
              "高（PromQL/YAML/Grafana パネル）",
              "no"
            ],
            [
              "低〜中",
              ""
            ]
          ],
          [
            [
              "リモートターミナル",
              ""
            ],
            [
              "内蔵（ポート開放不要 ＋ セッション録画再生 ＋ コマンド監査）",
              "yes"
            ],
            [
              "踏み台サーバーの別途導入が必要",
              "no"
            ],
            [
              "なし",
              "no"
            ],
            [
              "なし",
              "no"
            ]
          ],
          [
            [
              "ポート転送",
              ""
            ],
            [
              "内蔵（TCP / UDP / HTTP ＋ ポート範囲一括 · ポート開放不要）",
              "yes"
            ],
            [
              "なし",
              "no"
            ],
            [
              "なし",
              "no"
            ],
            [
              "なし",
              "no"
            ]
          ],
          [
            [
              "自動化運用",
              ""
            ],
            [
              "内蔵の Playbook 編成",
              "yes"
            ],
            [
              "なし（Ansible 等との併用が必要）",
              "no"
            ],
            [
              "なし",
              "no"
            ],
            [
              "なし",
              "no"
            ]
          ],
          [
            [
              "SRE クローズドループ",
              ""
            ],
            [
              "内蔵（インシデント / 自動修復 / SLO / チケット）",
              "yes"
            ],
            [
              "なし",
              "no"
            ],
            [
              "なし",
              "no"
            ],
            [
              "一部（PagerDuty 等の統合が必要）",
              ""
            ]
          ],
          [
            [
              "ログ収集・検索",
              ""
            ],
            [
              "内蔵（増分収集 ＋ 全文検索）",
              "yes"
            ],
            [
              "なし",
              "no"
            ],
            [
              "Loki / ELK が必要",
              "no"
            ],
            [
              "一部",
              ""
            ]
          ],
          [
            [
              "AI 運用アシスタント",
              ""
            ],
            [
              "内蔵（巡検診断 ＋ 自律エージェント ＋ RAG 知識庫、LLM 接続可）",
              "yes"
            ],
            [
              "なし",
              "no"
            ],
            [
              "なし",
              "no"
            ],
            [
              "一部（有料）",
              ""
            ]
          ],
          [
            [
              "アラート通知",
              ""
            ],
            [
              "Feishu/DingTalk/メール ＋ アリ/ファーウェイ/テンセントクラウド多クラウド SMS と音声通話 ＋ デスクトップ通知",
              "yes"
            ],
            [
              "メール/Webhook（設定が必要）",
              ""
            ],
            [
              "AlertManager（別途導入が必要）",
              "no"
            ],
            [
              "メール/Slack/Webhook",
              ""
            ]
          ],
          [
            [
              "ユーザー権限",
              ""
            ],
            [
              "RBAC ＋ MFA（内蔵）",
              "yes"
            ],
            [
              "ユーザーグループ（MFA なし）",
              "no"
            ],
            [
              "ネイティブなし（Grafana 企業版が必要）",
              "no"
            ],
            [
              "あり",
              ""
            ]
          ],
          [
            [
              "操作監査",
              ""
            ],
            [
              "ターミナル録画 ＋ 再生 ＋ コマンド監査",
              "yes"
            ],
            [
              "ターミナル監査なし",
              "no"
            ],
            [
              "なし",
              "no"
            ],
            [
              "ターミナル監査なし",
              "no"
            ]
          ],
          [
            [
              "GPU 監視",
              ""
            ],
            [
              "NVIDIA ＋ AMD ＋ Apple",
              "yes"
            ],
            [
              "カスタムテンプレートが必要",
              "no"
            ],
            [
              "DCGM Exporter が必要",
              "no"
            ],
            [
              "一部対応",
              ""
            ]
          ],
          [
            [
              "クロスプラットフォーム Agent",
              ""
            ],
            [
              "Linux/Win/macOS ＋ ARM64",
              "yes"
            ],
            [
              "Linux/Win/macOS",
              ""
            ],
            [
              "Linux/Win（macOS はコミュニティ）",
              "no"
            ],
            [
              "Linux/Win",
              "no"
            ]
          ],
          [
            [
              "PWA モバイル",
              ""
            ],
            [
              "対応（スマホのホーム画面にインストール可）＋ ネイティブ Android App",
              "yes"
            ],
            [
              "Web のみ",
              "no"
            ],
            [
              "Web のみ",
              "no"
            ],
            [
              "App あり（SaaS 専用）",
              ""
            ]
          ],
          [
            [
              "ネイティブ Android App",
              ""
            ],
            [
              "Kotlin ＋ Jetpack Compose（ホスト総覧/アラート/ターミナル/レポート）",
              "yes"
            ],
            [
              "なし",
              "no"
            ],
            [
              "なし",
              "no"
            ],
            [
              "あり（SaaS 専用）",
              ""
            ]
          ],
          [
            [
              "ハードウェア巡検（Redfish）",
              ""
            ],
            [
              "内蔵（標準 Redfish ＋ Huawei iBMC 互換）",
              "yes"
            ],
            [
              "IPMI プラグインが必要",
              "no"
            ],
            [
              "なし",
              "no"
            ],
            [
              "一部（有料）",
              ""
            ]
          ],
          [
            [
              "NetFlow トラフィック分析",
              ""
            ],
            [
              "内蔵（v5/v9/IPFIX ＋ 5タプル TOP-N）",
              "yes"
            ],
            [
              "なし",
              "no"
            ],
            [
              "別途 Exporter が必要",
              "no"
            ],
            [
              "なし",
              "no"
            ]
          ],
          [
            [
              "OceanStor ストレージ収集",
              ""
            ],
            [
              "内蔵（RESTful API でストレージプール/LUN/コントローラー/アラートを収集）",
              "yes"
            ],
            [
              "なし",
              "no"
            ],
            [
              "なし",
              "no"
            ],
            [
              "なし",
              "no"
            ]
          ],
          [
            [
              "マルチサーバー通知",
              ""
            ],
            [
              "単一 Agent から複数サーバーへブロードキャスト",
              "yes"
            ],
            [
              "非対応",
              "no"
            ],
            [
              "Remote Write が必要",
              "no"
            ],
            [
              "非対応",
              "no"
            ]
          ],
          [
            [
              "ゲートウェイ中継モード",
              ""
            ],
            [
              "内蔵（クロスセグメント透過）",
              "yes"
            ],
            [
              "Proxy/Agent の能動接続が必要",
              "no"
            ],
            [
              "Pushgateway が必要",
              "no"
            ],
            [
              "非対応",
              "no"
            ]
          ],
          [
            [
              "マシン指紋認証",
              ""
            ],
            [
              "machine-id ＋ MAC バインド",
              "yes"
            ],
            [
              "PSK/Token",
              "no"
            ],
            [
              "mTLS",
              "no"
            ],
            [
              "Agent Key",
              "no"
            ]
          ],
          [
            [
              "gzip 圧縮",
              ""
            ],
            [
              "内蔵（8〜10 倍圧縮）",
              "yes"
            ],
            [
              "Nginx 設定が必要",
              "no"
            ],
            [
              "Nginx 設定が必要",
              "no"
            ],
            [
              "あり",
              ""
            ]
          ],
          [
            [
              "リレーショナル / 監査ストレージ",
              ""
            ],
            [
              "PostgreSQL（設定/イベント/チケット/監査をすべて永続化）",
              "yes"
            ],
            [
              "MySQL / PostgreSQL",
              ""
            ],
            [
              "なし（メトリクスのみ）",
              "no"
            ],
            [
              "クラウドホスティング",
              ""
            ]
          ],
          [
            [
              "データ自主 / プライベート",
              ""
            ],
            [
              "完全プライベート · データは社内から出ない",
              "yes"
            ],
            [
              "プライベート",
              ""
            ],
            [
              "プライベート",
              ""
            ],
            [
              "データはクラウドへ",
              "no"
            ]
          ],
          [
            [
              "価格",
              ""
            ],
            [
              "無料オープンソース（AGPL-3.0）",
              "yes"
            ],
            [
              "無料オープンソース（GPL）",
              ""
            ],
            [
              "無料オープンソース（Apache）",
              ""
            ],
            [
              "ホスト台数課金",
              "no"
            ]
          ],
          [
            [
              "適した規模",
              ""
            ],
            [
              "数台 → 万単位のホスト（VM が担う）",
              "yes"
            ],
            [
              "50〜5000+ 台",
              ""
            ],
            [
              "100〜10000+ 台",
              ""
            ],
            [
              "任意（従量課金）",
              ""
            ]
          ],
          [
            [
              "アラートノイズ削減と階層化",
              ""
            ],
            [
              "重大/警告の2階層 ＋ 重複排除・冷却でアラート量を約80%削減",
              "yes"
            ],
            [
              "ネイティブなノイズ削減なし",
              ""
            ],
            [
              "AlertManager ＋ カスタムが必要",
              ""
            ],
            [
              "あり（有料）",
              ""
            ]
          ],
          [
            [
              "データ保存方針",
              ""
            ],
            [
              "永続保存、自動期限切れや削除なし",
              "yes"
            ],
            [
              "パーティションテーブル/アーカイブが必要",
              ""
            ],
            [
              "ローカル TSDB は限定",
              ""
            ],
            [
              "クラウド方針",
              ""
            ]
          ],
          [
            [
              "企業サポートとサービス",
              ""
            ],
            [
              "オープンソースコミュニティ ＋ メールサポート、オプションで企業導入コンサル/研修",
              "yes"
            ],
            [
              "コミュニティ中心",
              ""
            ],
            [
              "コミュニティ中心",
              ""
            ],
            [
              "有料サポート",
              ""
            ]
          ]
        ]
      },
      "advantages": [
        {
          "title": "統合クローズドループ、5つ以上のツールスタックを置換",
          "color": "ok",
          "icon": "M13 2L3 14h9l-1 8 10-12h-9l1-8z",
          "desc": [
            "単一バイナリ ＝ メトリクス ＋ アラート ＋ ログ ＋ ターミナル ＋ Playbook ＋ SRE ハブ ＋ AI 運用アシスタント。Prometheus ＋ Grafana ＋ Alertmanager ＋ ELK ＋ 踏み台サーバー ＋ チケットシステムをつなぎ合わせる必要なし。",
            "収集、アラート、排障、修復、振り返りが同一プラットフォームでクローズドループ。ツールチェーンとデータが分断されない。"
          ],
          "value": "「5つ以上のツールをつなぐ」から「1つのプラットフォームで全部」へ"
        },
        {
          "title": "エンタープライズ級ストレージ、ワンクリック起動",
          "color": "accent",
          "icon": "M5 13l4 4L19 7",
          "desc": [
            "docker compose 1つでサーバー ＋ PostgreSQL ＋ VictoriaMetrics を同時に起動、3分で本稼働 —— 業界標準のストレージを使いつつ、DB / TSDB を手作業で組む手間を省く。",
            "データはすべてあなたの社内ネットワークに残り、規模に応じて数台から万単位のホストへスムーズに拡張。設定キーは AES-256-GCM で静止暗号化され、クラウドに上げず、ロックインもしない。"
          ],
          "value": "業界標準ストレージの安心感 ＋ ワンクリック導入の手軽さ"
        },
        {
          "title": "無料かつオープンソース",
          "color": "purple",
          "icon": "M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6",
          "desc": [
            "AGPL-3.0 オープンソースライセンスで商用制限なし。コードは GitHub でホスティングされ、透明で信頼できる。ホスト台数制限なし、機能の削減なし、「企業版」のような手口なし。",
            "Python プラグイン SDK で自由に拡張。数行のコードでカスタムメトリクスを接続。コミュニティの貢献で継続的に改善。"
          ],
          "value": "ライセンス料ゼロ、ホスト台数制限ゼロ、機能ロックインゼロ"
        }
      ]
    }
  },
  "faq": {
    "ja": {
      "page.title": "よくある質問 — AIOps",
      "page.desc": "AIOps の導入、セキュリティ、性能、拡張、ポート転送について、最もよくある疑問をまとめました。",
      "page.oglocale": "ja_JP",
      "head.tag": "よくある質問",
      "head.title": "AIOps について、知りたいことは",
      "head.desc": "導入、セキュリティ、性能、拡張 —— 最もよくある疑問をまとめました",
      "items": [
        {
          "q": "AIOps は無料ですか？機能制限はありますか？",
          "a": "完全に無料で、AGPL-3.0 オープンソースライセンスを採用。ホスト台数制限なし、機能の削減なし、「企業版」のような手口なし。コードは GitHub でホスティングされ、透明で信頼できます。"
        },
        {
          "q": "データベースやミドルウェアの別途導入は必要ですか？",
          "a": "PostgreSQL（設定/イベント/チケット/監査などのリレーショナルデータを担う）＋ VictoriaMetrics（メトリクス/トレンドなどの時系列データを担う）が必要です —— ただし両者とも docker compose がサーバーとともにワンクリックで起動するため、手作業での構築や個別の保守は不要です。サーバーと Agent 自体は、第三者依存ゼロの単一 Go バイナリです。"
        },
        {
          "q": "どの OS とアーキテクチャに対応していますか？",
          "a": "Agent は Linux、Windows、macOS をネイティブ対応し、AMD64 と ARM64 をカバーします。サーバーは単一の Go バイナリで、1 コア 1GB の小さなクラウドサーバーでも動作します。"
        },
        {
          "q": "ポートを開かなくても、リモートターミナルとポート転送はどう動作するのですか？",
          "a": "Agent が能動的にサーバーへリバース接続します。すべての通信（ターミナル、転送、上報）はこの確立されたトンネルを通り、ホストはいかなる入站ポートも開く必要がなく、ファイアウォール / NAT 環境に自然に適合します。"
        },
        {
          "q": "何台のホストを監視できますか？性能はどうですか？",
          "a": "設計目標は 1〜5000+ 台のホスト。収集は5秒間隔、gzip 8〜10 倍圧縮、メトリクスは永続保存。単一サーバーのリソース消費は極めて低く、横方向にはマルチサーバー通知で拡張できます。"
        },
        {
          "q": "Zabbix / Prometheus と比べた優位性はどこにありますか？",
          "a": "単一バイナリに監視、アラート、ログ、ターミナル、Playbook、SRE ハブ、AI 巡検を内蔵し、5つ以上のツールスタックを置換。業界標準の PostgreSQL ＋ VictoriaMetrics で保存し、compose で一式をワンクリック起動、3分で本稼働。学習曲線は PromQL/YAML 体系よりはるかに低い。"
        },
        {
          "q": "ポート転送はどのプロトコルに対応していますか？一度に連続したポートを転送できますか？",
          "a": "TCP / UDP / HTTP の3プロトコルの単一ポート転送に対応：TCP はデータベース、SSH、Web 管理画面などに；UDP は DNS、ゲーム、映像音声などのデータグラム型サービスに；HTTP はステートレスなプロキシトンネルを通り、ブラウザから直接インネットワークのページにアクセス可能。さらに TCP / UDP はポート範囲の一括転送にも対応 —— 開始・終了ポートを指定するだけで連続ポートを一度にマッピング（1バッチ最大100ポート）。同一バッチは1グループにまとめられ、グループ単位で一括有効 / 無効 / 削除でき、1件ずつ操作する必要はありません。"
        },
        {
          "q": "データはどこに保存されますか？クラウドにアップロードされますか？",
          "a": "すべてのデータはあなたが導入したサーバーのローカルに保存され、自ホスティングで、いかなるクラウドサービスにも依存せず、外部に送信もされません。データ主権を重視するシナリオに適しています。"
        },
        {
          "q": "どうやってアップグレードしますか？アップグレードは監視中のホストに影響しますか？",
          "a": "アップグレードは新バージョンのイメージを取得してコンテナを再起動する（またはバイナリを置き換えて再起動）だけ。データは PostgreSQL ＋ VictoriaMetrics に永続化され、コンテナの破棄に伴わず。Agent は切断時の自動再接続と、既知の指紋によるトークンレス再登録に対応し、サーバー再起動後も監視が中断しません。"
        },
        {
          "q": "AI 巡検と診断に、別途の大規模モデルや API Key の設定は必要ですか？",
          "a": "必要ありません。AI Provider が未設定の場合は内蔵のヒューリスティックルールがフォールバックとして研判し、導入即稼働。LLM を設定するとエージェント級の分析に昇格します。エラー / アラートログは自動的に分析コンテキストに組み込まれ、現場に即した判断が可能になります。"
        },
        {
          "q": "アラートが多すぎて疲弊しませんか？どうやってノイズを削減しますか？",
          "a": "重大 / 警告の2階層 ＋ イベントの重複排除・冷却（デフォルトは5分以内の同一イベントを重複通知しない）に対応し、ノイズ抑制と組み合わせることでアラート量を約80%削減。さらに保守 / 標準 / 緩和の3段階しきい値プリセットを備え、シナリオに応じてワンクリックで切り替え。"
        },
        {
          "q": "既存の監視スタック（Prometheus / Grafana / Zabbix）とどう共存できますか？",
          "a": "すべてを作り直す必要はありません。AIOps はターミナル、Playbook、監査、AI 診断などの不足分を補完。VictoriaMetrics は Prometheus のリモート読み書きと互換性があり、既存のダッシュボードと併存可能。マルチサーバー通知により、新旧の監視間をスムーズに移行・必要に応じて移行できます。"
        },
        {
          "q": "データはどのくらい保持されますか？エクスポートできますか？",
          "a": "すべての監視データ（メトリクス / ログ / 監査）は永続保存され、自動期限切れや削除はされません。操作ログと監査は CSV エクスポートに対応。PostgreSQL と VictoriaMetrics はいずれもあなたの社内ネットワークにあり、いつでもバックアップと移行ができ、データ主権を完全に掌握できます。"
        },
        {
          "q": "大規模（5000+ 台）導入ではどうチューニングしますか？",
          "a": "5秒間隔収集 ＋ gzip 8〜10 倍圧縮 ＋ メトリクス永続保存で、単一サーバーのリソース消費は極めて低い。横方向にはマルチサーバー通知で拡張。ゲートウェイ中継により出口の長接続数を減らし、弱いネットワーク環境でも安定して管理できます。"
        },
        {
          "q": "企業版 / 商用サポート / 等級保護（等保）コンサルは提供していますか？",
          "a": "製品自体は完全に無料のオープンソースで、機能の削減なし。併せてエンタープライズ級サポートも提供：プライベート導入、カスタム開発、方案設計、研修などで、メールでお問い合わせいただけます。等級保護（等保）関連の監査、権限、MFA の機能はすべて内蔵されています。"
        },
        {
          "q": "私の監視データはどこに保存されますか？第三者にアップロードされますか？",
          "a": "すべてのデータはあなた自身の PostgreSQL と VictoriaMetrics に保存され、自有的サーバーまたは社内ネットワークに導入され、いかなる第三者クラウドにも絶対にアップロードされません。プラットフォームは 100% プライベート自ホスティングで、データは永久に自社保有、いつでもエクスポート可能。"
        },
        {
          "q": "Agent は入站ポートを開く必要がありますか？ファイアウォールはどう設定しますか？",
          "a": "必要ありません。Agent が能動的にサーバーへ「リバース接続」して上報と指示の取得を行うため、サーバーは Agent に対していかなる入站ポートも開く必要がない。管理者が Web パネル / ターミナルにアクセスする場合のみ、サーバーポート（デフォルト 8529）を開通すればよい。これは公開ネットワークのサーバー ＋ 社内ネットワークの Agent というトポロジーに自然に適合します。"
        },
        {
          "q": "AI 診断 / 巡検に追加料金や外部の大規模モデルは必要ですか？",
          "a": "AI 機能は内蔵かつ無料で、プラグイン可能な LLM アーキテクチャを採用：自ホスティングのオープンソースモデル（Ollama / vLLM など）にも、パブリッククラウドの API にも接続可能。検索拡張（RAG）はあなた自身の pgvector 記憶庫に基づき、事例と知識は社内ネットワークから出ません。"
        },
        {
          "q": "モバイル端末はありますか？どの OS に対応していますか？",
          "a": "ネイティブの Android App（Kotlin ＋ Jetpack Compose、20以上の画面）と HarmonyOS NEXT App（ArkTS）を提供。いずれも同一バックエンドに接続。インストールパッケージは別配布（モバイルソースは本リポジトリに含まれません）。リアルタイム総覧、アラート通知、モバイルターミナル、AI アシスタントに対応。通知は自前の /ws/push 長接続を使用。"
        },
        {
          "q": "どうやって高可用性を実現しますか？1つの Agent で複数のサーバーに上報できますか？",
          "a": "可能です。Agent はマルチサーバー（servers[]）設定に対応：1回の収集を複数のサーバーへ並行して上報し、リトライとサーキットブレーカーと組み合わせてマルチDC 災害復旧（DR）を実現します。サーバー自体はステートレス（メモリ状態とセッションを除く）で、リバースプロキシ / ロードバランサーの背後に置いて横方向に拡張可能。ストレージ層の PostgreSQL と VictoriaMetrics はそれぞれ高可用性方案を提供します。"
        },
        {
          "q": "セキュリティと等級保護（等保）コンプライアンス監査の面ではどうですか？",
          "a": "セッション Cookie ＋ RBAC（admin/operator/viewer）、TOTP MFA、ターミナル二次パスワード、Agent マシン指紋、インストールトークン（7日の猶予期間）、設定キーの AES-256-GCM 静止暗号化とオプションの TLS を提供。全操作は監査ログに書き込まれます。関連機能は等級保護（等保）監査とコンプライアンス要件に合致し、監査の証拠チェーンを構成する一部として活用できます。"
        }
      ],
      "cta.title": "まだ疑問がありますか？まず手を動かして試してみてください",
      "cta.desc": "3分で導入、すべての機能が導入即稼働。気に入らなければいつでもアンインストールでき、外部依存は残りません。",
      "cta.btn1": "無料で導入 →",
      "cta.btn2": "機能の詳細を見る"
    }
  },
  "contact": {
    "ja": {
      "page.title": "お問い合わせ — AIOps",
      "page.desc": "協業、導入、カスタマイズ、フィードバックのご要望がありますか？メールまたは GitHub で AIOps チームにご連絡ください。",
      "page.oglocale": "ja_JP",
      "head.tag": "お問い合わせ",
      "head.title": "いつでも喜んでお手伝いします",
      "head.desc": "導入相談、機能提案、ビジネス協業、問題のフィードバックなど、どうぞご連絡ください",
      "c.email.title": "電子メール",
      "c.email.desc": "ビジネス協業、導入相談、カスタム開発、一般的なご質問にはメールが最も確実です。通常 1〜2 営業日以内に返信します。",
      "c.email.btn": "メールを送る",
      "c.issue.title": "問題のフィードバック",
      "c.issue.desc": "バグ発見や明確な機能要望がありますか？GitHub Issues で投稿してください。追跡・議論可能で、チームが公開で対応します。",
      "c.issue.btn": "Issue を投稿",
      "c.repo.title": "オープンソースコミュニティ",
      "c.repo.desc": "プロジェクトの動向をフォロー、ドキュメントを参照、議論に参加、あるいはコードを貢献。Star や Fork を歓迎します。一緒により良い製品に。",
      "c.repo.btn": "リポジトリへ",
      "resp.title": "私たちの約束",
      "resp.i1.t": "1〜2 営業日",
      "resp.i1.d": "メールは通常 2 営業日以内に返信",
      "resp.i2.t": "公開・透明",
      "resp.i2.d": "Issues と議論はすべて公開で追跡可能",
      "resp.i3.t": "真摯に対応",
      "resp.i3.d": "いただいた提案とフィードバックはすべて評価",
      "subscribe.title": "製品動向を購読",
      "subscribe.desc": "メールアドレスを登録すれば、新バージョン、導入のコツ、ベストプラクティスをいち早くお届けします。",
      "subscribe.placeholder": "あなたのメールアドレス *",
      "subscribe.btn": "購読",
      "subscribe.note": "プライバシーを尊重し、迷惑メールは一切送信しません。",
      "subscribe.ok": "購読ありがとうございます！メールで最新情報をお届けします。",
      "subscribe.invalid": "有効なメールアドレスを入力してください。",
      "subscribe.phonePlaceholder": "電話番号（任意）",
      "subscribe.storageErr": "保存に失敗しました。ブラウザのストレージがいっぱいです。",
      "subscribe.dup": "すでに購読済みです。最新動向をタイムリーにお届けします！",
      "ent.tag": "エンタープライズサービス",
      "ent.title": "エンタープライズ導入のために生まれた",
      "ent.desc": "プライベート導入から大規模運用まで、企業シナリオに即したコンサル、導入、研修を提供。選定、POC、規模拡張のいずれの段階でも、適したサポートを見つけられます。",
      "ent.deploy.tag": "導入形態",
      "ent.deploy.title": "3つの導入形態、規模に応じて",
      "ent.deploy.items": [
        {
          "t": "プライベート標準版",
          "d": "単一ノードの docker compose でワンクリック起動。中小チームと単一DCに適し、3分で本稼働、外部依存ゼロ。",
          "icon": "M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"
        },
        {
          "t": "高可用クラスタ版",
          "d": "マルチサーバー通知 ＋ マルチDC 災害復旧。重要業務の監視を中断なく。マルチDCと大規模ホスト管理に適。",
          "icon": "M12 2L2 7v10l10 5 10-5V7L12 2z"
        },
        {
          "t": "ハイブリッドクラウド管理",
          "d": "ゲートウェイ中継でクロスセグメント / ファイアウォール越えのホストをつなぎ、本社と拠点、クラウドとオンプレを1画面で統一監視。",
          "icon": "M17 1l4 4-4 4 M3 11V9a4 4 0 0 1 4-4h14 M7 23l-4-4 4-4 M21 13v2a4 4 0 0 1-4 4H3"
        }
      ],
      "ent.support.tag": "サポートと SLA",
      "ent.support.title": "層別サポート、必要に応じて選択",
      "ent.support.items": [
        {
          "t": "コミュニティ版（無料）",
          "d": "GitHub オープンソースコミュニティ ＋ 完全なドキュメント。問題は Issue で公開フォロー。AGPL-3.0 ライセンスで機能削減なし。",
          "icon": "M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"
        },
        {
          "t": "標準サポート",
          "d": "メール優先対応（1〜2 営業日）＋ 導入相談とベストプラクティス。すでに本稼働し安定稼働を求めるチームに適。",
          "icon": "M5 13l4 4L19 7"
        },
        {
          "t": "エンタープライズサポート",
          "d": "専任ソリューションアーキテクト、プライベート導入、カスタム開発、研修。等級保護（等保）コンプライアンスと大規模導入を支援。",
          "icon": "M9 12l2 2 4-4 M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z"
        }
      ],
      "ent.process.tag": "協業の流れ",
      "ent.process.title": "4ステップでエンタープライズ運用を開始",
      "ent.process.items": [
        {
          "n": "01",
          "t": "要件ヒアリング",
          "d": "規模、アーキテクチャ、痛みを把握し、監視とコンプライアンスの目標を明確に。"
        },
        {
          "n": "02",
          "t": "ソリューション設計",
          "d": "プライベート / クラスタ / ハイブリッド導入の提案と統合方案を提示。"
        },
        {
          "n": "03",
          "t": "POC 検証",
          "d": "実環境で小規模に試験導入し、重要機能を検証。"
        },
        {
          "n": "04",
          "t": "本稼働と研修",
          "d": "全量本稼働 ＋ チーム研修で、持続可能な運用プロセスを構築。"
        }
      ],
      "ent.trust.tag": "なぜ私たちを選ぶか",
      "ent.trust.title": "企業が気にすることを、私たちはとっくに考慮",
      "ent.trust.items": [
        {
          "t": "データ主権",
          "d": "完全なプライベート導入。データは社内から出ず、キーは AES-256-GCM で暗号化。",
          "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4"
        },
        {
          "t": "セキュリティ・コンプライアンス",
          "d": "RBAC ＋ MFA ＋ ターミナル監査 ＋ 操作ログで、等級保護（等保）監査の遡及可能性要件に合致。",
          "icon": "M9 12l2 2 4-4 M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z"
        },
        {
          "t": "オープンソース・透明性",
          "d": "AGPL-3.0 ライセンスでコードは公開・監査可能。ロックインなし、隠れた課金なし。",
          "icon": "M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"
        },
        {
          "t": "スムーズな拡張",
          "d": "単機から万単位のホストまで、マルチサーバー通知とゲートウェイ中継で弾性拡張。",
          "icon": "M13 2L3 14h9l-1 8 10-12h-9l1-8z"
        }
      ],
      "cta.title": "始める準備はできましたか？",
      "cta.desc": "3分で導入完了、すべての機能が導入即稼働。ご質問はいつでもメールで。",
      "cta.btn1": "無料で導入 →",
      "cta.btn2": "機能の詳細を見る"
    }
  }
};

  /* 渲染辅助 */
  function esc(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }
  function onCell(v) {
    var on = (v === "✓" || v === true);
    return '<td class="cmp-cell ' + (on ? "on" : "off") + '">' + esc(on ? "✓" : (v || "—")) + "</td>";
  }

  /* 定价页渲染 */
  window.__renderPricing = function (T, lang) {
    var d = (T.pricing && (T.pricing[lang] || T.pricing["zh-CN"])) || {};
    var cards = document.getElementById("priceCards");
    if (cards && d.plans) {
      cards.innerHTML = d.plans.map(function (p) {
        var feats = (p.features || []).map(function (f) {
          return '<li class="price-feat ' + (f.ok ? "yes" : "no") + '"><span class="pf-mark">' + (f.ok ? "✓" : "—") + '</span><span>' + esc(f.t) + "</span></li>";
        }).join("");
        var ext = p.ctaHref && p.ctaHref.indexOf("http") === 0 ? ' target="_blank" rel="noopener"' : "";
        return '<div class="price-card' + (p.highlight ? " hl" : "") + ' reveal">'
          + '<div class="price-card-head">'
          + (p.highlight ? '<span class="price-badge">' + esc(d.recommendLabel || "推荐") + "</span>" : "")
          + '<div class="price-name">' + esc(p.name) + "</div>"
          + (p.bestFor ? '<div class="price-best">' + esc(p.bestFor) + "</div>" : "")
          + '<div class="price-price">' + esc(p.price) + '<span class="price-unit">' + esc(p.unit || "") + "</span></div>"
          + '<p class="price-desc">' + esc(p.desc) + "</p>"
          + "</div>"
          + '<ul class="price-feats">' + feats + "</ul>"
          + '<a class="btn-primary price-cta" href="' + esc(p.ctaHref) + '"' + ext + ">" + esc(p.cta) + "</a>"
          + "</div>";
      }).join("");
    }
    var cmp = document.getElementById("priceCompare");
    if (cmp && d.matrix && d.matrix.groups) {
      cmp.innerHTML = d.matrix.groups.map(function (g) {
        var rows = g.rows.map(function (r) {
          return '<tr><td class="cmp-label">' + esc(r.label) + "</td>" + onCell(r.c) + onCell(r.s) + onCell(r.e) + "</tr>";
        }).join("");
        return '<div class="cmp-group reveal"><h3 class="cmp-group-title">' + esc(g.group) + "</h3>"
          + '<div class="cmp-table-wrap"><table class="price-compare-table">'
          + "<thead><tr><th></th><th>" + esc(d.colCommunity || "社区版") + "</th><th>" + esc(d.colStandard || "标准支持") + "</th><th>" + esc(d.colEnterprise || "企业支持") + "</th></tr></thead>"
          + "<tbody>" + rows + "</tbody></table></div></div>";
      }).join("") + (d.footNote ? '<p class="price-foot reveal">' + esc(d.footNote) + "</p>" : "");
    }
  };

  /* 案例页渲染 */
  window.__renderCases = function (T, lang) {
    var d = (T.cases && (T.cases[lang] || T.cases["zh-CN"])) || {};
    var list = document.getElementById("caseList");
    if (list && d.items) {
      list.innerHTML = d.items.map(function (it) {
        var tags = (it.tags || []).map(function (t) { return '<span class="case-tag">' + esc(t) + "</span>"; }).join("");
        var results = (it.results || []).map(function (r) {
          return '<div class="case-result"><div class="cr-val">' + esc(r.value) + '</div><div class="cr-label">' + esc(r.label) + "</div></div>";
        }).join("");
        return '<div class="case-card reveal">'
          + '<div class="case-card-head"><span class="case-icon"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="' + esc(it.icon) + '"/></svg></span>'
          + '<span class="case-industry">' + esc(it.industry) + "</span></div>"
          + '<p class="case-summary">' + esc(it.summary || "") + "</p>"
          + '<div class="case-narrative">'
          + '<div class="case-block"><div class="case-block-label">' + esc(d.secBackground || "行业背景") + '</div><p class="case-block-text">' + esc(it.background || "") + "</p></div>"
          + '<div class="case-block case-block-pain"><div class="case-block-label">' + esc(d.secPain || "痛点分析") + '</div><p class="case-block-text">' + esc(it.pain || "") + "</p></div>"
          + '<div class="case-block case-block-sol"><div class="case-block-label">' + esc(d.secSolution || "解决方案") + '</div><p class="case-block-text">' + esc(it.solution || "") + "</p></div>"
          + "</div>"
          + '<div class="case-tags">' + tags + "</div>"
          + '<div class="case-results">' + results + "</div>"
          + "</div>";
      }).join("");
    }
    var testi = document.getElementById("caseTesti");
    if (testi) {
      var idx = (T.index && (T.index[lang] || T.index["zh-CN"])) || {};
      /* flat keys: proof.q1 / proof.q1by (not nested proof.q1) */
      var quotes = [];
      if (idx["proof.q1"]) quotes.push({ q: idx["proof.q1"], by: idx["proof.q1by"] || "" });
      if (idx["proof.q2"]) quotes.push({ q: idx["proof.q2"], by: idx["proof.q2by"] || "" });
      if (d.testimonials && d.testimonials.length) {
        quotes = d.testimonials.map(function (t) {
          return { q: t.quote || t.q || "", by: t.by || t.role || "" };
        }).filter(function (x) { return x.q; });
      }
      var title = d.testimonialsTitle ? '<h3 class="testi-title reveal">' + esc(d.testimonialsTitle) + "</h3>" : "";
      var grid = '<div class="testi-grid">' + quotes.map(function (item) {
        var by = item.by ? '<footer class="testi-by">' + esc(item.by) + "</footer>" : "";
        return '<div class="testi-card reveal"><div class="testi-quote">' + esc(item.q) + "</div>" + by + "</div>";
      }).join("") + "</div>";
      testi.innerHTML = title + grid;
    }
  };

})();
