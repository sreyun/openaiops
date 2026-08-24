/* ============================================================
   AIOps · 多语言 i18n 系统
   支持简体中文(zh-CN) / 繁体中文(zh-TW) / 英文(en) / 日本語(ja) / 한국어(ko)
   ============================================================ */
"use strict";
(function(){

var SUPPORTED = ["zh-CN", "zh-TW", "en", "ja", "ko"];
var DEFAULT_LANG = "zh-CN";

/* 语言显示名称 */
var LANG_NAMES = {
  "zh-CN": "简体中文",
  "zh-TW": "繁體中文",
  "en": "English",
  "ja": "日本語",
  "ko": "한국어"
};

/* ============================================================
   翻译字典 — 按页面分区
   ============================================================ */
var T = {

/* ---------- 通用（导航栏 + 页脚）---------- */
"_common": {
  "zh-CN": {
    "nav.home": "首页", "nav.features": "功能详情", "nav.solutions": "解决方案", "nav.comparison": "产品对比", "nav.faq": "常见问题", "nav.contact": "联系我们",
    "nav.cta": "免费部署", "nav.deploy": "立即部署 →", "nav.seePain": "了解痛点",
    "cta.copy": "复制命令", "cta.copied": "已复制",
    "footer.desc": "企业级主机监控与 SRE 运维平台。开源免费，PostgreSQL + VictoriaMetrics 统一存储，一条命令部署。",
    "footer.product": "产品", "footer.resources": "资源",
    "footer.docs": "使用文档", "footer.install": "安装指南",
    "footer.github": "GitHub 仓库",
    "footer.copy": "© 2026 AIOps · AGPL-3.0 License · Built with Go"
  },
  "zh-TW": {
    "nav.home": "首頁", "nav.features": "功能詳情", "nav.solutions": "解決方案", "nav.comparison": "產品對比", "nav.faq": "常見問題", "nav.contact": "聯絡我們",
    "nav.cta": "免費部署", "nav.deploy": "立即部署 →", "nav.seePain": "了解痛點",
    "cta.copy": "複製命令", "cta.copied": "已複製",
    "footer.desc": "企業級主機監控與 SRE 運維平台。開源免費，PostgreSQL + VictoriaMetrics 統一存儲，一條命令部署。",
    "footer.product": "產品", "footer.resources": "資源",
    "footer.docs": "使用文檔", "footer.install": "安裝指南",
    "footer.github": "GitHub 倉庫",
    "footer.copy": "© 2026 AIOps · AGPL-3.0 License · Built with Go"
  },
  "en": {
    "nav.home": "Home", "nav.features": "Features", "nav.solutions": "Solutions", "nav.comparison": "Comparison", "nav.faq": "FAQ", "nav.contact": "Contact",
    "nav.cta": "Deploy Free", "nav.deploy": "Deploy Now →", "nav.seePain": "See Pain Points",
    "cta.copy": "Copy", "cta.copied": "Copied",
    "footer.desc": "Enterprise host monitoring & SRE ops platform. Open source, unified PostgreSQL + VictoriaMetrics storage, one-command deploy.",
    "footer.product": "Product", "footer.resources": "Resources",
    "footer.docs": "Docs", "footer.install": "Install Guide",
    "footer.github": "GitHub",
    "footer.copy": "© 2026 AIOps · AGPL-3.0 License · Built with Go"
  }
},

/* ---------- 首页 ---------- */
"index": {
  "zh-CN": {
    "page.title": "AIOps — 开源可观测与 SRE 平台，一个二进制替代 Zabbix + Prometheus + Grafana",
    "page.desc": "一个二进制替代 Zabbix + Prometheus + Grafana + Alertmanager + 自动化剧本 + 堡垒机。AIOps 是 100% 开源、私有化自托管的企业级可观测与 SRE 平台：实时监控、智能告警、远程终端/桌面、自动化自愈、AI 巡检诊断、MCP 集成、SRE 闭环与 Android / HarmonyOS 移动控制台，一条命令部署，数据永久自持。",
    "page.oglocale": "zh_CN",
    "hero.badge": "100% 开源 · AGPL-3.0 · 3 分钟完成部署",
    "hero.title": '别再让运维团队<br>被<span class="gradient-text">告警疲劳与半夜救火</span>拖垮',
    "hero.desc": "单个 Go 二进制 + 零依赖 Agent：监控、告警、远程终端/桌面、自动化自愈、SRE 闭环、AI 巡检/MCP、Android / HarmonyOS 移动控制台 —— 一条命令上线，数据完全自持。",
    "hero.creds": "首次登录强制修改用户名+密码，建议为管理员账户启用 MFA",
    "hero.proof": "开源免费 · 一条命令部署 · 数据永久自持",
    "hero.positioning": "私有化部署，告警到自愈一条闭环，把半夜救火变成按时下班。",
    "hero.trust1": "AGPL-3.0 开源",
    "hero.trust2": "私有化自托管",
    "hero.trust3": "PG + VictoriaMetrics",
    "hero.trust4": "等保审计能力",
    "cta.copy": "复制命令",
    "cta.copied": "已复制",
    "hero.quickStart": "5 分钟快速开始",
    "hero.docs": "文档",
    "hero.changelog": "更新日志",
    "hero.stat1.num": "5000+", "hero.stat1.label": "稳定支撑主机",
    "hero.stat2.num": "3", "hero.stat2.label": "完成部署", "hero.stat2.unit": "min",
    "hero.stat3.num": "4", "hero.stat3.label": "Linux/Win/macOS/麒麟", "hero.stat3.unit": "平台",
    "hero.stat4.num": "100", "hero.stat4.label": "开源免费", "hero.stat4.unit": "%",
    "hero.titleNew": '一个二进制<br>替代 <span class="gradient-text">Zabbix + Prometheus + Grafana</span>',
    "pain.tag": "这些场景，你一定不陌生", "pain.title": "中小团队运维，逃不开的四大难题",
    "pain.desc": "人力有限、工具分散、告警轰炸、排查靠人肉 —— 这些问题正在悄悄吃掉你团队的效率与睡眠",
    "pain1.title": "人力严重不足",
    "pain1.desc": "1-2 个运维管几十台到上百台机器，日常巡检、故障处理、安全补丁全靠人堆，加班成常态。",
    "pain1.sol": "一条命令自动部署 Agent，批量纳管，典型团队反馈人力投入显著下降",
    "pain2.title": "告警疲劳轰炸",
    "pain2.desc": "每台机器一堆监控项，告警铺天盖地却分不清轻重缓急，真正的严重故障被淹没在噪音里。",
    "pain2.sol": "分级告警（严重/警告）+ 去重冷却 + 桌面通知，只推真正需要处理的",
    "pain3.title": "故障排查耗时",
    "pain3.desc": "出问题先 SSH 上去敲命令查日志，定位全靠经验。多人协作时谁做了什么完全没记录。",
    "pain3.sol": "远程终端免开端口 + 会话回放 + 操作审计，故障 5 分钟定位",
    "pain4.title": "监控工具碎片化",
    "pain4.desc": "Prometheus 管指标、Grafana 看图、Alertmanager 告警、Jira 工单 —— 五六个工具拼起来，部署和维护成本高昂。",
    "pain4.sol": "一个二进制搞定监控+告警+终端+自动化，替代 Zabbix + Prometheus + Grafana + Alertmanager",
    "feat.tag": "核心能力", "feat.title": "一个平台，覆盖监控到 SRE 的全链路",
    "feat.desc": "从采集、告警、终端/桌面、剧本，到 SRE 中枢、日志检索、AI 巡检、MCP 工具集成 —— 一个二进制闭环，无需拼接多个工具，也无需养一支专门维护监控栈的团队。",
    "feat1.title": "实时监控与趋势", "feat1.desc": "CPU / 内存 / SWAP / 多磁盘 / 网络 / TCP / 负载 / 进程 / GPU —— 5 秒级采集，交互式趋势图，时序统一存入 VictoriaMetrics。", "feat1.val": "不用逐台 SSH 看状态，一处看全量",
    "feat2.title": "多云智能告警与消息中心", "feat2.desc": "27 维阈值自定义 + 分级去重冷却；飞书/钉钉/邮件 + 阿里云/华为云/腾讯云短信与语音电话多渠道推送；站内消息中心汇聚事件 / AI 诊断 / 自动修复 / 工单，一键直达。", "feat2.val": "关键告警电话打醒人，处置不漏项",
    "feat3.title": "SRE 中枢", "feat3.desc": "事件闭环（告警 / SLO / 手动汇聚 + 时间线）· 告警→剧本自动修复（护栏 + 人工审批）· SLO / 错误预算 · 工单流转。", "feat3.val": "从告警到修复一站闭环",
    "feat3.visualTitle": "告警 → 诊断 → 自愈 → 复盘", "feat3.visualDesc": "全流程自动化闭环，无需人工拼接",
    "feat4.title": "日志采集 + AI 巡检诊断", "feat4.desc": "Agent 增量采集日志 + 全文检索；AI 定时巡检 + 根因研判；支持 MCP Streamable HTTP，把值班/诊断工具接到 Cursor / Claude；语音配置可一键自测播报与识别回环。", "feat4.val": "根因定位，从人肉经验到智能研判",
    "feat5.title": "远程终端与自动化剧本", "feat5.desc": "浏览器全 TTY 免开端口 + 终端二次密码 + 录制回放 + 只读旁观；剧本可视化编排，批量并行下发到多台主机。", "feat5.val": "故障排查从 30 分钟缩短到 5 分钟",
    "feat6.title": "企业级存储与安全", "feat6.desc": "PostgreSQL + VictoriaMetrics 统一存储；RBAC + MFA + 配置密钥 AES-256-GCM 静态加密 + 可选 TLS 加密传输；四平台原生采集（含麒麟），AMD64 + ARM64 全覆盖。", "feat6.val": "数据自持 + 静态加密，契合等保审计",
    "arch.tag": "工作原理", "arch.title": "一套架构，覆盖从采集到运维的闭环", "arch.desc": "Agent 反向连接免开端口，数据汇聚到单二进制服务端，告警 / 终端 / 剧本一站完成",
    "arch.linux": "Linux Agent", "arch.linuxSub": "/proc + syscall 原生采集",
    "arch.win": "Windows Agent", "arch.winSub": "Win32 API + ConPTY 终端",
    "arch.mac": "macOS Agent", "arch.macSub": "sysctl + Apple GPU",
    "arch.serverTitle": "AIOps 服务端", "arch.serverSub": "单二进制 · PG + VM 统一存储",
    "arch.cap1": "指标 + 日志", "arch.cap2": "告警 + 消息中心", "arch.cap3": "SRE 中枢 + 自动修复", "arch.cap4": "AI 巡检诊断", "arch.cap5": "终端 / 剧本 / RBAC",
    "arch.panel": "浏览器实时面板 + Android / HarmonyOS", "arch.panelSub": "PWA · 多端访问 · 原生移动控制台",
    "arch.notify": "飞书 / 钉钉 / 邮件 / 短信 / 语音", "arch.notifySub": "分级告警推送",
    "arch.multi": "多服务端 / 中继", "arch.multiSub": "跨机房容灾 · 跨网段穿透",
    "arch.hw": "硬件巡检 / NetFlow / OceanStor", "arch.hwSub": "Redfish · 流量分析 · 存储采集",
    "cta.title": "三分钟，让运维轻下来",
    "cta.demo": "预约演示",
    "cta.desc": "下载 docker-compose.yml，一条命令自动生成并写入密钥，docker compose 一键启动。无需手动改配置，无需额外数据库中间件。",
    "cta.cmd": "# 通过 GitHub 下载<br>bash &lt;(curl -fsSL https://raw.githubusercontent.com/sreyun/aiops-monitor/master/scripts/secure-compose.sh)<br><br># 通过 Gitee 镜像下载（GitHub 访问受限时推荐）<br>bash &lt;(curl -fsSL https://gitee.com/bigdatasafe/aiops-monitor/raw/master/scripts/secure-compose.sh)<br><br># 启动（密钥已写入，无需手动修改）<br>docker compose up -d<br><span style='color:var(--muted)'># 浏览器打开 http://localhost:8529</span>",
    "cta.btn2": "查看功能详情",
    "trust.tag": "原生支持",
    "trust.title": "完美融入你现有的技术栈",
    "trust.desc": "主流操作系统、容器化部署与团队协作工具开箱即用，无需改造基础设施",
    "proof.usage": "已稳定支撑 1–5000+ 台主机",
    "proof.usageSub": "从个人项目到中型企业，一套平台平滑扩展",
    "proof.item2num": "PG + VM",
    "proof.item2label": "双引擎统一存储",
    "proof.item3num": "数据自持", "proof.item3label": "内网私有 · 永久留存",
    "proof.item4num": "27 维", "proof.item4label": "智能阈值 · 分级告警",
    "proof.q1": "“以前 Zabbix、Prometheus、Grafana 各管一摊，严重告警总在半夜漏。换成 AIOps 后，阈值、自愈、工单全串成一个闭环，告警终于有人管、有下文。”",
    "proof.q1by": "某中型电商 · SRE Lead · ~200 台主机",
    "proof.q2": "“远程终端 + 会话回放 + 操作审计，故障定位从半小时压到 5 分钟，半夜被叫起来的次数肉眼可见地少了。”",
    "proof.q2by": "某金融科技 · 运维负责人 · 多机房部署",
    "proof.q3": "“一条命令部署，3 分钟跑起来，不用养专人维护监控栈。对我们这种 10 人以下的团队来说，这就是救星。”",
    "proof.q3by": "某 SaaS 创业公司 · CTO · ~30 台主机",
    "proof.q4": "“从 Zabbix 迁移过来比想象中简单，数据不丢、配置可复用。最惊喜的是剧本自愈，重复性故障基本不用人介入了。”",
    "proof.q4by": "某制造业 IT · 运维主管 · 跨 3 省机房",
    "integrations": [
      {"name":"Linux","icon":"M9 3v2M15 3v2M5 7h14a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V9a2 2 0 0 1 2-2z"},
      {"name":"Windows","icon":"M3 5h8v6H3zM13 5h8v6h-8zM3 13h8v6H3zM13 13h8v6h-8z"},
      {"name":"macOS","icon":"M12 2a10 10 0 1 0 10 10A10 10 0 0 0 12 2zM2 12h20M12 2a15 15 0 0 1 0 20 15 15 0 0 1 0-20z"},
      {"name":"Docker","icon":"M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"},
      {"name":"Python","icon":"M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4"},
      {"name":"飞书","icon":"M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z"},
      {"name":"钉钉","icon":"M8 12a4 4 0 1 0 8 0 4 4 0 0 0-8 0zM3 12h2M19 12h2M12 3v2M12 19v2"},
      {"name":"SMTP 邮件","icon":"M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6"}
    ],
    "fwd.tag": "独家能力",
    "fwd.title": "浏览器内端口转发，安全暴露内网服务",
    "fwd.desc": "无需在公网开放任何端口，通过 Agent 反向隧道把内网 Web / 数据库 / 调试接口安全地映射到本地浏览器。支持 TCP / UDP / HTTP 三种协议的单端口转发，以及 TCP / UDP 端口范围批量转发；列表与卡片双视图，启用 / 禁用 / 复制 / 编辑 / 删除一键完成。",
    "fwd.term1": "# TCP 单端口：内网数据库映射到本地",
    "fwd.term2": "# UDP + 端口范围：一次映射整段端口",
    "fwd.term3": "✓ 11 rules · 同组统一启停 · 走 Agent 隧道",
    "fwd.points": [
      "TCP / UDP / HTTP 三协议单端口转发，覆盖数据库、Web 后台、DNS / 游戏 / 音视频等场景",
      "TCP / UDP 端口范围批量转发，一次映射连续端口段（单批最多 100 个），整组启停删除",
      "Agent 反向连接，内网服务零公网暴露",
      "列表 / 卡片双视图，转发状态一目了然",
      "启用 / 禁用 / 复制 / 编辑 / 删除，运维操作闭环",
      "转发统计与健康检测，异常及时感知"
    ],
    "fwd.cta": "查看全部功能",
    "android.tag": "移动运维",
    "android.title": "把运维中心装进口袋",
    "android.desc": "原生 Android / HarmonyOS 控制台让你随时掌握主机状态、处理告警，并远程登录终端排障；安装包独立分发（移动端源码不在本仓库）。",
    "android.p1": "实时主机总览：CPU / 内存 / 磁盘 / 网络一眼尽览，多主机流畅切换",
    "android.p2": "告警即时推送：严重告警手机弹窗，点开即看详情与处置建议",
    "android.p3": "随时随地远程终端：内置安全终端，手机也能 SSH 排障，会话可回放审计",
    "android.p4": "原生流畅体验：Kotlin + Jetpack Compose 打造，无 WebView 套壳",
    "android.p5": "私有化自托管：自定义服务器地址，数据始终留在你的内网",
    "android.badge1": "Android / 鸿蒙",
    "android.badge2": "私有化部署",
    "android.cta": "查看移动端能力 →",
    "android.m1l": "CPU 负载",
    "android.m2l": "内存使用",
    "android.m3l": "在线主机",
    "android.m1v": "23% 正常",
    "android.m2v": "61%",
    "android.m3v": "12 台",
    "android.m4": "终端已连接 · db-prod-01",
    "faq.tag": "常见问题",
    "faq.title": "关于 AIOps，你可能想问",
    "faq.desc": "部署、安全、性能、扩展 —— 我们整理了最常见的疑问",
    "faq.viewAll": "查看全部常见问题 →"
  },
  "zh-TW": {
    "page.title": "AIOps — 開源可觀測與 SRE 平台，一個二進制替代 Zabbix + Prometheus + Grafana",
    "page.desc": "一個二進制替代 Zabbix + Prometheus + Grafana + Alertmanager + 自動化劇本 + 堡壘機。AIOps 是 100% 開源、私有化自託管的企業級可觀測與 SRE 平台：即時監控、智能告警、遠端終端/桌面、自動化自愈、AI 巡檢診斷、MCP 整合、SRE 閉環與 Android / HarmonyOS 行動控制台，一條命令部署，資料永久自持。",
    "page.oglocale": "zh_TW",
    "hero.badge": "100% 開源 · 資料自持 · 3 分鐘完成部署",
    "hero.title": '別再讓運維團隊<br>被<span class="gradient-text">告警疲勞與半夜救火</span>拖垮',
    "hero.desc": "AIOps 用單個 Go 二進制 + 零依賴 Agent，覆蓋從採集、告警、遠端終端/桌面、自動化自愈，到 SRE 閉環、AI 巡檢/MCP、與 Android / HarmonyOS 行動控制台的運維全鏈路。PostgreSQL + VictoriaMetrics 雙存儲，資料完全自持；一條命令部署，3 分鐘上線。",
    "hero.creds": '首次登入強制修改使用者名稱+密碼，建議為管理員帳戶啟用 MFA',
    "hero.proof": "100% 開源 · 一條命令部署 · 資料永久自持",
    "hero.positioning": "私有化部署，告警到自癒一條閉環，把半夜救火變成按時下班。",
    "hero.quickStart": "5 分鐘快速開始",
    "hero.docs": "文件",
    "hero.changelog": "更新日誌",
    "hero.stat1.num": "5000+", "hero.stat1.label": "穩定支撐主機",
    "hero.stat2.num": "3", "hero.stat2.label": "完成部署", "hero.stat2.unit": "min",
    "hero.stat3.num": "4", "hero.stat3.label": "Linux/Win/macOS/麒麟", "hero.stat3.unit": "平台",
    "hero.stat4.num": "100", "hero.stat4.label": "開源免費", "hero.stat4.unit": "%",
    "hero.titleNew": '一個二進制<br>替代 <span class="gradient-text">Zabbix + Prometheus + Grafana</span>',
    "pain.tag": "這些場景，你一定不陌生", "pain.title": "中小團隊運維，逃不開的四大難題",
    "pain.desc": "人力有限、工具分散、告警轟炸、排查靠人肉 —— 這些問題正在悄悄吃掉你團隊的效率與睡眠",
    "pain1.title": "人力嚴重不足",
    "pain1.desc": "1-2 個運維管幾十台到上百台機器，日常巡檢、故障處理、安全補丁全靠人堆，加班成常態。",
    "pain1.sol": "一條命令自動部署 Agent，批量納管，運維人力立省 70%（內部實測）",
    "pain2.title": "告警疲勞轟炸",
    "pain2.desc": "每台機器一堆監控項，告警鋪天蓋地卻分不清輕重緩急，真正的嚴重故障被淹沒在噪音裡。",
    "pain2.sol": "分級告警（嚴重/警告）+ 去重冷卻 + 桌面通知，只推真正需要處理的",
    "pain3.title": "故障排查耗時",
    "pain3.desc": "出問題先 SSH 上去敲命令查日誌，定位全靠經驗。多人協作時誰做了什麼完全沒記錄。",
    "pain3.sol": "遠程終端免開端口 + 會話回放 + 操作審計，故障 5 分鐘定位",
    "pain4.title": "監控工具碎片化",
    "pain4.desc": "Prometheus 管指標、Grafana 看圖、Alertmanager 告警、Jira 工單 —— 五六個工具拼起來，部署和維護成本高昂。",
    "pain4.sol": "一個二進制搞定監控+告警+終端+自動化，替代 Zabbix + Prometheus + Grafana + Alertmanager",
    "feat.tag": "核心能力", "feat.title": "一個平台，覆蓋監控到 SRE 的全鏈路",
    "feat.desc": "從採集、告警、終端/桌面、劇本，到 SRE 中樞、日誌檢索、AI 巡檢、MCP 工具整合 —— 一個二進制閉環，無需拼接多個工具，也無需養一支專門維護監控棧的團隊。",
    "feat1.title": "即時監控與趨勢", "feat1.desc": "CPU / 記憶體 / SWAP / 多磁碟 / 網路 / TCP / 負載 / 進程 / GPU —— 5 秒級採集，互動式趨勢圖，時序統一存入 VictoriaMetrics。", "feat1.val": "不用逐台 SSH 看狀態，一處看全量",
    "feat2.title": "多雲智能告警與消息中心", "feat2.desc": "27 維閾值自定義 + 分級去重冷卻；飛書/釘釘/郵件 + 阿里雲/華為雲/騰訊雲簡訊與語音電話多渠道推送；站內消息中心匯聚事件 / AI 診斷 / 自動修復 / 工單，一鍵直達。", "feat2.val": "關鍵告警電話打醒人，處置不漏項",
    "feat3.title": "SRE 中樞", "feat3.desc": "事件閉環（告警 / SLO / 手動匯聚 + 時間線）· 告警→劇本自動修復（護欄 + 人工審批）· SLO / 錯誤預算 · 工單流轉。", "feat3.val": "從告警到修復一站閉環",
    "feat3.visualTitle": "告警 → 診斷 → 自愈 → 復盤", "feat3.visualDesc": "全流程自動化閉環，無需人工拼接",
    "feat4.title": "日誌採集 + AI 巡檢診斷", "feat4.desc": "Agent 增量採集日誌 + 全文檢索；AI 定時巡檢 + 根因研判；支援 MCP Streamable HTTP，把值班/診斷工具接到 Cursor / Claude；語音設定可一鍵自測播報與識別回環。", "feat4.val": "根因定位，從人肉經驗到智能研判",
    "feat5.title": "遠程終端與自動化劇本", "feat5.desc": "瀏覽器全 TTY 免開端口 + 終端二次密碼 + 錄製回放 + 唯讀旁觀；劇本可視化編排，批量並行下發到多台主機。", "feat5.val": "故障排查從 30 分鐘縮短到 5 分鐘",
    "feat6.title": "企業級存儲與安全", "feat6.desc": "PostgreSQL + VictoriaMetrics 統一存儲；RBAC + MFA + 配置密鑰 AES-256-GCM 靜態加密 + 可選 TLS 加密傳輸；四平台原生採集（含麒麟），AMD64 + ARM64 全覆蓋。", "feat6.val": "資料自持 + 靜態加密，契合等保審計",
    "arch.tag": "運作原理", "arch.title": "一套架構，覆蓋從採集到運維的閉環", "arch.desc": "Agent 反向連接免開端口，數據匯聚到單二進制服務端，告警 / 終端 / 劇本一站完成",
    "arch.linux": "Linux Agent", "arch.linuxSub": "/proc + syscall 原生採集",
    "arch.win": "Windows Agent", "arch.winSub": "Win32 API + ConPTY 終端",
    "arch.mac": "macOS Agent", "arch.macSub": "sysctl + Apple GPU",
    "arch.serverTitle": "AIOps 服務端", "arch.serverSub": "單二進制 · PG + VM 統一存儲",
    "arch.cap1": "指標 + 日誌", "arch.cap2": "告警 + 消息中心", "arch.cap3": "SRE 中樞 + 自動修復", "arch.cap4": "AI 巡檢診斷", "arch.cap5": "終端 / 劇本 / RBAC",
    "arch.panel": "瀏覽器即時面板 + Android / HarmonyOS", "arch.panelSub": "PWA · 多端訪問 · 原生行動控制台",
    "arch.notify": "飛書 / 釘釘 / 郵件 / 簡訊 / 語音", "arch.notifySub": "分級告警推送",
    "arch.multi": "多服務端 / 中繼", "arch.multiSub": "跨機房容災 · 跨網段穿透",
    "arch.hw": "硬體巡檢 / NetFlow / OceanStor", "arch.hwSub": "Redfish · 流量分析 · 儲存採集",
    "cta.title": "三分鐘，讓運維輕下來",
    "cta.demo": "預約演示",
    "cta.desc": "下載 docker-compose.yml，一條命令自動生成並寫入密鑰，docker compose 一鍵啟動。無需手動改配置，無需額外資料庫中間件。",
    "cta.cmd": "# 透過 GitHub 下載<br>bash &lt;(curl -fsSL https://raw.githubusercontent.com/sreyun/aiops-monitor/master/scripts/secure-compose.sh)<br><br># 透過 Gitee 鏡像下載（GitHub 存取受限時推薦）<br>bash &lt;(curl -fsSL https://gitee.com/bigdatasafe/aiops-monitor/raw/master/scripts/secure-compose.sh)<br><br># 啟動（密鑰已寫入，無需手動修改）<br>docker compose up -d<br><span style='color:var(--muted)'># 瀏覽器開啟 http://localhost:8529</span>",
    "cta.btn2": "查看功能詳情",
    "trust.tag": "技術生態",
    "trust.title": "完美融入你現有的技術棧",
    "trust.desc": "原生支持主流作業系統、容器化部署與團隊協作工具，開箱即用，無需改造你的基礎設施",
    "proof.usage": "已穩定支撐 1–5000+ 台主機",
    "proof.usageSub": "從個人專案到中大型企業，一套平台平滑擴展",
    "proof.item2num": "PG + VM",
    "proof.item2label": "雙引擎統一存儲",
    "proof.item3num": "資料自持", "proof.item3label": "內網私有 · 永久留存",
    "proof.item4num": "27 維", "proof.item4label": "智能閾值 · 分級告警",
    "proof.q1": "「以前 Zabbix、Prometheus、Grafana 各管一攤，嚴重告警總在半夜漏。換成 AIOps 後，閾值、自愈、工單全串成一個閉環，告警終於有人管、有下文。」",
    "proof.q1by": "某中型電商 · SRE Lead · ~200 台主機",
    "proof.q2": "「遠端終端 + 會話回放 + 操作審計，故障定位從半小時壓到 5 分鐘，半夜被叫起來的次數肉眼可見地少了。」",
    "proof.q2by": "某金融科技 · 運維負責人 · 多機房部署",
    "proof.q3": "「一條命令部署，3 分鐘跑起來，不用養專人維護監控棧。對我們這種 10 人以下的團隊來說，這就是救星。」",
    "proof.q3by": "某 SaaS 創業公司 · CTO · ~30 台主機",
    "proof.q4": "「從 Zabbix 遷移過來比想像中簡單，資料不丟、配置可複用。最驚喜的是劇本自愈，重複性故障基本不用人介入了。」",
    "proof.q4by": "某製造業 IT · 運維主管 · 跨 3 省機房",
    "hero.trust1": "AGPL-3.0 開源",
    "hero.trust2": "私有化自託管",
    "hero.trust3": "PG + VictoriaMetrics",
    "hero.trust4": "等保審計能力",
    "cta.copy": "複製命令",
    "cta.copied": "已複製",
    "integrations": [
      {"name":"Linux","icon":"M9 3v2M15 3v2M5 7h14a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V9a2 2 0 0 1 2-2z"},
      {"name":"Windows","icon":"M3 5h8v6H3zM13 5h8v6h-8zM3 13h8v6H3zM13 13h8v6h-8z"},
      {"name":"macOS","icon":"M12 2a10 10 0 1 0 10 10A10 10 0 0 0 12 2zM2 12h20M12 2a15 15 0 0 1 0 20 15 15 0 0 1 0-20z"},
      {"name":"Docker","icon":"M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"},
      {"name":"Python","icon":"M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4"},
      {"name":"飛書","icon":"M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z"},
      {"name":"釘釘","icon":"M8 12a4 4 0 1 0 8 0 4 4 0 0 0-8 0zM3 12h2M19 12h2M12 3v2M12 19v2"},
      {"name":"SMTP 郵件","icon":"M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6"}
    ],
    "fwd.tag": "獨家能力",
    "fwd.title": "瀏覽器內端口轉發，安全暴露內網服務",
    "fwd.desc": "無需在公網開放任何端口，透過 Agent 反向隧道把內網 Web / 資料庫 / 除錯介面安全地映射到本地瀏覽器。支援 TCP / UDP / HTTP 三種協定的單端口轉發，以及 TCP / UDP 端口範圍批量轉發；列表與卡片雙視圖，啟用 / 停用 / 複製 / 編輯 / 刪除一鍵完成。",
    "fwd.term1": "# TCP 單端口：內網資料庫映射到本地",
    "fwd.term2": "# UDP + 端口範圍：一次映射整段端口",
    "fwd.term3": "✓ 11 rules · 同組統一啟停 · 走 Agent 隧道",
    "fwd.points": [
      "TCP / UDP / HTTP 三協定單端口轉發，覆蓋資料庫、Web 後台、DNS / 遊戲 / 音視訊等場景",
      "TCP / UDP 端口範圍批量轉發，一次映射連續端口段（單批最多 100 個），整組啟停刪除",
      "Agent 反向連接，內網服務零公網暴露",
      "列表 / 卡片雙視圖，轉發狀態一目了然",
      "啟用 / 停用 / 複製 / 編輯 / 刪除，運維操作閉環",
      "轉發統計與健康檢測，異常及時感知"
    ],
    "fwd.cta": "查看全部功能",
    "android.tag": "移動運維",
    "android.title": "把運維中心裝進口袋",
    "android.desc": "原生 Android / HarmonyOS 控制台讓你隨時掌握主機狀態、處理告警，並遠端登入終端排障；安裝包獨立分發（行動端原始碼不在本倉庫）。",
    "android.p1": "即時主機總覽：CPU / 記憶體 / 磁碟 / 網路一眼盡覽，多主機流暢切換",
    "android.p2": "告警即時推送：嚴重告警手機彈窗，點開即看詳情與處置建議",
    "android.p3": "隨時隨地遠程終端：內建安全終端，手機也能 SSH 排障，會話可回放審計",
    "android.p4": "原生流暢體驗：Kotlin + Jetpack Compose 打造，無 WebView 套殼",
    "android.p5": "私有化自託管：自定義伺服器位址，資料始終留在你的內網",
    "android.badge1": "Android / 鴻蒙",
    "android.badge2": "私有化部署",
    "android.cta": "查看移動端能力 →",
    "android.m1l": "CPU 負載",
    "android.m2l": "記憶體使用",
    "android.m3l": "在線主機",
    "android.m1v": "23% 正常",
    "android.m2v": "61%",
    "android.m3v": "12 台",
    "android.m4": "終端已連線 · db-prod-01",
    "faq.tag": "常見問題",
    "faq.title": "關於 AIOps，你可能想問",
    "faq.desc": "部署、安全、效能、擴展 —— 我們整理了最常見的疑問",
    "faq.viewAll": "查看全部常見問題 →"
  },
  "en": {
    "page.title": "AIOps — Open-Source Observability & SRE Platform: One Binary Replacing Zabbix + Prometheus + Grafana",
    "page.desc": "One binary replaces Zabbix + Prometheus + Grafana + Alertmanager + automation playbooks + your bastion host. AIOps is a 100% open-source, self-hosted enterprise observability & SRE platform: real-time monitoring, smart alerts, remote terminal/desktop, automated self-healing, AI inspection, MCP integration, an SRE closed loop, and native Android / HarmonyOS consoles — deploy with one command, your data stays yours forever.",
    "page.oglocale": "en_US",
    "hero.badge": "100% Open Source · You Own Your Data · Deploy in 3 Minutes",
    "hero.title": 'Stop letting your ops team<br>drown in <span class="gradient-text">alert fatigue and 3 a.m. fire-fighting</span>',
    "hero.desc": "AIOps pairs a single Go binary with a zero-dependency agent to cover the entire ops chain — collection, alerts, remote terminal/desktop, automated self-healing, the SRE loop, AI inspection & MCP, and native Android / HarmonyOS consoles. Dual storage on PostgreSQL + VictoriaMetrics keeps your data fully self-hosted; one command, three minutes to live.",
    "hero.creds": 'First login forces a username + password change; enabling MFA for the admin account is recommended',
    "hero.proof": "100% Open Source · One-Command Deploy · Your Data, Forever",
    "hero.positioning": "Private deployment, a closed loop from alert to self-healing — turn midnight firefighting into clocking off on time.",
    "hero.quickStart": "5-Min Quick Start",
    "hero.docs": "Docs",
    "hero.changelog": "Changelog",
    "hero.stat1.num": "5000+", "hero.stat1.label": "Hosts supported",
    "hero.stat2.num": "3", "hero.stat2.label": "To deploy", "hero.stat2.unit": "min",
    "hero.stat3.num": "4", "hero.stat3.label": "Linux/Win/macOS/Kylin", "hero.stat3.unit": "platforms",
    "hero.stat4.num": "100", "hero.stat4.label": "Open source", "hero.stat4.unit": "%",
    "hero.titleNew": 'One binary<br>replacing <span class="gradient-text">Zabbix + Prometheus + Grafana</span>',
    "pain.tag": "These scenarios probably feel familiar", "pain.title": "Four Headaches Every Small Ops Team Knows",
    "pain.desc": "Too few people, too many tools, alert bombardment, manual troubleshooting — quietly draining your team's efficiency and sleep.",
    "pain1.title": "Severely Understaffed",
    "pain1.desc": "1-2 ops engineers managing dozens to hundreds of machines. Daily inspections, incident response, security patches — all manual, overtime is the norm.",
    "pain1.sol": "One-command agent deployment, bulk onboarding, save 70% ops effort (internal testing)",
    "pain2.title": "Alert Fatigue",
    "pain2.desc": "Each machine has dozens of metrics. Alerts flood in with no way to distinguish critical from noise. Real incidents get buried.",
    "pain2.sol": "Tiered alerts (critical/warning) + dedup cooldown + desktop notifications",
    "pain3.title": "Slow Troubleshooting",
    "pain3.desc": "SSH in, run commands, check logs — diagnosis depends on experience. No record of who did what during collaborative debugging.",
    "pain3.sol": "Port-free remote terminal + session replay + command audit, 5-min root cause",
    "pain4.title": "Fragmented Tooling",
    "pain4.desc": "Prometheus for metrics, Grafana for dashboards, Alertmanager for alerts, Jira for tickets — five or six tools stitched together at high cost.",
    "pain4.sol": "One binary replaces monitoring + alerts + terminal + automation — say goodbye to Zabbix + Prometheus + Grafana + Alertmanager",
    "feat.tag": "Core Capabilities", "feat.title": "One Platform, from Monitoring to SRE",
    "feat.desc": "From collection, alerts, terminal/desktop and playbooks to the SRE hub, log search, AI inspection and MCP tools — one closed-loop binary. No tool stitching, and no dedicated team just to keep the monitoring stack alive.",
    "feat1.title": "Real-time Monitoring & Trends", "feat1.desc": "CPU / Memory / SWAP / Disk / Network / TCP / Load / Process / GPU — 5-second collection, interactive trend charts, time-series stored in VictoriaMetrics.", "feat1.val": "Stop SSH-ing into every host — see everything in one place",
    "feat2.title": "Multi-Cloud Smart Alerts & Message Center", "feat2.desc": "27 customizable thresholds + tiered dedup cooldown; Feishu/DingTalk/Email plus SMS & voice call across Aliyun/Huawei/Tencent Cloud; an in-app message center aggregating incidents / AI diagnosis / auto-remediation / tickets, one click away.", "feat2.val": "Critical alerts can call and wake on-call, nothing missed",
    "feat3.title": "SRE Hub", "feat3.desc": "Incident closed loop (alert / SLO / manual + timeline) · alert→playbook auto-remediation (guardrails + approval) · SLO / error budget · ticket workflow.", "feat3.val": "From alert to fix, one closed loop",
    "feat3.visualTitle": "Alert → Diagnose → Self-heal → Review", "feat3.visualDesc": "Full automation loop, no manual stitching needed",
    "feat4.title": "Log Collection + AI Inspection", "feat4.desc": "Agent incremental log tailing + full-text search; scheduled AI inspection + RCA; MCP Streamable HTTP exposes duty/diagnosis tools to Cursor / Claude; speech settings include one-click TTS/STT self-test.", "feat4.val": "Root cause: from tribal knowledge to AI judgment",
    "feat5.title": "Remote Terminal & Playbooks", "feat5.desc": "Browser full TTY with no inbound ports + terminal secondary password + recording replay + read-only observe; visual playbook orchestration, batch parallel dispatch.", "feat5.val": "Troubleshooting from 30 min to 5 min",
    "feat6.title": "Enterprise Storage & Security", "feat6.desc": "Unified PostgreSQL + VictoriaMetrics storage; RBAC + MFA + config-secret AES-256-GCM encryption at rest + optional TLS; native collection on 4 platforms (incl. Kylin), AMD64 + ARM64.", "feat6.val": "Self-hosted data + encryption at rest, audit-ready",
    "arch.tag": "How It Works", "arch.title": "One Architecture, Full Ops Loop", "arch.desc": "Agents connect reversely (no open ports). Data converges to a single binary server. Alerts, terminal, and playbooks — all in one place.",
    "arch.linux": "Linux Agent", "arch.linuxSub": "/proc + syscall native collection",
    "arch.win": "Windows Agent", "arch.winSub": "Win32 API + ConPTY terminal",
    "arch.mac": "macOS Agent", "arch.macSub": "sysctl + Apple GPU",
    "arch.serverTitle": "AIOps Server", "arch.serverSub": "Single binary · unified PG + VM storage",
    "arch.cap1": "Metrics + Logs", "arch.cap2": "Alerts + Messages", "arch.cap3": "SRE Hub + Auto-remediation", "arch.cap4": "AI Inspection", "arch.cap5": "Terminal / Playbooks / RBAC",
    "arch.panel": "Browser Dashboard + Android / HarmonyOS", "arch.panelSub": "PWA · multi-device · native mobile consoles",
    "arch.notify": "Feishu / DingTalk / Email / SMS / Voice", "arch.notifySub": "Tiered alert push",
    "arch.multi": "Multi-Server / Relay", "arch.multiSub": "Cross-DC DR · cross-subnet tunnel",
    "arch.hw": "HW Inspection / NetFlow / OceanStor", "arch.hwSub": "Redfish · traffic analysis · storage collection",
    "cta.title": "Make Ops Lighter in 3 Minutes",
    "cta.demo": "Book a Demo",
    "cta.desc": "Download docker-compose.yml; one command auto-generates and writes the secrets, then docker compose brings up the full stack (server + PostgreSQL + VictoriaMetrics). No manual config edits.",
    "cta.cmd": "# Via GitHub<br>bash &lt;(curl -fsSL https://raw.githubusercontent.com/sreyun/aiops-monitor/master/scripts/secure-compose.sh)<br><br># Via Gitee mirror (recommended if GitHub is slow)<br>bash &lt;(curl -fsSL https://gitee.com/bigdatasafe/aiops-monitor/raw/master/scripts/secure-compose.sh)<br><br># Start (secrets already written, no manual edit)<br>docker compose up -d<br><span style='color:var(--muted)'># Open http://localhost:8529 in your browser</span>",
    "cta.btn2": "View Features",
    "trust.tag": "Tech Ecosystem",
    "trust.title": "Fits Right Into Your Existing Stack",
    "trust.desc": "Native support for mainstream operating systems, containerized deployment, and team-collaboration tools — ready out of the box, no infrastructure changes needed",
    "proof.usage": "Battle-tested on 1–5000+ hosts",
    "proof.usageSub": "From personal projects to mid-size enterprises, one platform scales smoothly",
    "proof.item2num": "PG + VM",
    "proof.item2label": "Unified dual-engine storage",
    "proof.item3num": "Self-hosted", "proof.item3label": "Your intranet, your data",
    "proof.item4num": "27 dims", "proof.item4label": "Smart thresholds · tiered alerts",
    "proof.q1": "“We used to run Zabbix, Prometheus, and Grafana in separate silos, and critical alerts kept slipping through at 3 a.m. With AIOps, thresholds, self-healing, and tickets form one closed loop — alerts finally have an owner and a trail.”",
    "proof.q1by": "Mid-size e-commerce · SRE Lead · ~200 hosts",
    "proof.q2": "“Remote terminal plus session replay and command audit took our troubleshooting from 30 minutes to 5 — and the 3 a.m. wake-up calls dropped, visibly.”",
    "proof.q2by": "FinTech ops lead · multi-DC deployment",
    "proof.q3": "“One command to deploy, up in 3 minutes, no dedicated person to maintain the monitoring stack. For a team under 10, that's a lifesaver.”",
    "proof.q3by": "SaaS startup · CTO · ~30 hosts",
    "proof.q4": "“Migrating from Zabbix was easier than expected — no data loss, configs carried over. The playbook self-healing was the biggest surprise; repetitive faults barely need human intervention now.”",
    "proof.q4by": "Manufacturing IT · Ops Lead · 3-province DCs",
    "hero.trust1": "AGPL-3.0 Open Source",
    "hero.trust2": "Self-hosted",
    "hero.trust3": "PG + VictoriaMetrics",
    "hero.trust4": "Audit-ready",
    "cta.copy": "Copy",
    "cta.copied": "Copied",
    "integrations": [
      {"name":"Linux","icon":"M9 3v2M15 3v2M5 7h14a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V9a2 2 0 0 1 2-2z"},
      {"name":"Windows","icon":"M3 5h8v6H3zM13 5h8v6h-8zM3 13h8v6H3zM13 13h8v6h-8z"},
      {"name":"macOS","icon":"M12 2a10 10 0 1 0 10 10A10 10 0 0 0 12 2zM2 12h20M12 2a15 15 0 0 1 0 20 15 15 0 0 1 0-20z"},
      {"name":"Docker","icon":"M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"},
      {"name":"Python","icon":"M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4"},
      {"name":"Feishu","icon":"M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z"},
      {"name":"DingTalk","icon":"M8 12a4 4 0 1 0 8 0 4 4 0 0 0-8 0zM3 12h2M19 12h2M12 3v2M12 19v2"},
      {"name":"SMTP Email","icon":"M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6"}
    ],
    "fwd.tag": "Exclusive Capability",
    "fwd.title": "In-Browser Port Forwarding — Safely Expose Internal Services",
    "fwd.desc": "No public ports required. Through the agent's reverse tunnel, map internal web apps, databases, and debug endpoints securely to your local browser. Single-port forwarding over TCP / UDP / HTTP, plus TCP / UDP port-range batch forwarding; list and card dual views, with enable/disable/copy/edit/delete at your fingertips.",
    "fwd.term1": "# TCP single port: map internal DB to local",
    "fwd.term2": "# UDP + port range: map a whole range at once",
    "fwd.term3": "✓ 11 rules · unified start/stop per group · via Agent tunnel",
    "fwd.points": [
      "Single-port forwarding over TCP / UDP / HTTP — databases, web backends, DNS / gaming / media, and more",
      "TCP / UDP port-range batch forwarding — map a contiguous range at once (up to 100 ports per batch), manage the whole group together",
      "Agent reverse connection — zero public exposure for internal services",
      "List + card dual views — forwarding status at a glance",
      "Enable / disable / copy / edit / delete — a closed-loop ops workflow",
      "Forwarding stats and health checks — catch anomalies early"
    ],
    "fwd.cta": "View All Features",
    "android.tag": "Mobile Ops",
    "android.title": "Your ops center, in your pocket",
    "android.desc": "Native Android / HarmonyOS consoles keep host status and alerts at your fingertips, with secure remote terminal on the go. Packages are distributed externally (mobile source is not in this repo).",
    "android.p1": "Live host overview: CPU / memory / disk / network at a glance, with smooth multi-host switching",
    "android.p2": "Instant alert push: critical alerts pop up on your phone, tap to see details and remediation tips",
    "android.p3": "Remote terminal anywhere: built-in secure terminal lets you SSH from your phone, with session replay & audit",
    "android.p4": "Truly native: built with Kotlin + Jetpack Compose, no WebView wrapper",
    "android.p5": "Self-hosted: point to your own server, your data never leaves your network",
    "android.badge1": "Android / HarmonyOS",
    "android.badge2": "Self-hosted",
    "android.cta": "Explore mobile →",
    "android.m1l": "CPU Load",
    "android.m2l": "Memory",
    "android.m3l": "Online Hosts",
    "android.m1v": "23% OK",
    "android.m2v": "61%",
    "android.m3v": "12",
    "android.m4": "Terminal connected · db-prod-01",
    "faq.tag": "FAQ",
    "faq.title": "Common Questions About AIOps",
    "faq.desc": "Deployment, security, performance, extensibility — we've compiled the most common questions",
    "faq.viewAll": "View All FAQs →"
  }
},

/* ---------- 功能详情页 ---------- */
"features": {
  "zh-CN": {
    "page.title": "功能详情 — AIOps",
    "page.desc": "不用再拼 Zabbix + Prometheus + Grafana。AIOps 把监控、告警、远程终端、自动化自愈、AI 诊断与 SRE 闭环，按你真实的运维痛点组织成一个平台 —— 缺什么能力，一找就到。",
    "page.oglocale": "zh_CN",
    "head.tag": "功能详情",
    "head.title": "你需要的功能，按能解决的问题排好了",
    "head.desc": "不是功能清单，而是针对运维真实痛点的能力矩阵",
    "band.tag": "为运维真实痛点而生",
    "band.title": "这些功能，对应你每天都在头疼的事",
    "pains": [
      {
        "icon": "M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9",
        "t": "告警风暴",
        "d": "每天几百条告警，真正严重的反而被淹没"
      },
      {
        "icon": "M4 17l6-6-6-6M12 19h8",
        "t": "到不了现场",
        "d": "机房隔离、没开端口，出事只能干着急"
      },
      {
        "icon": "M21 21l-5.2-5.2M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0z",
        "t": "找不到根因",
        "d": "指标红了，却不知道为什么红"
      },
      {
        "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4",
        "t": "不敢上生产",
        "d": "私有化部署，安全合规与可靠性谁兜底"
      }
    ],
    "groups": [
      {
        "tag": "01",
        "title": "全栈可观测",
        "desc": "从操作系统到业务接口的端到端可见性",
        "pain": "系统到底健不健康，还要一台台 SSH 敲 top/free/df？",
        "roles": [
          "运维",
          "SRE",
          "开发者"
        ],
        "items": [
          {
            "title": "实时指标监控",
            "color": "accent",
            "icon": "M22 12h-4l-3 9L9 3l-3 9H2",
            "desc1": "CPU / 内存 / SWAP / 多磁盘 / 网络收发 / 系统负载 / 进程数 / TCP 连接数 / 运行时长 —— 5 秒级采集，全面覆盖。",
            "desc2": "指标永久存储，重启后续传不丢点。",
            "val": "告别逐台 SSH top/free/df 查看指标"
          },
          {
            "title": "GPU 监控",
            "color": "accent",
            "icon": "M9 3v2M15 3v2M9 19v2M15 19v2M3 9h2M3 15h2M19 9h2M19 15h2M9 9h6v6H9z",
            "desc1": "NVIDIA（nvidia-smi）、AMD（Linux sysfs）、Apple（macOS ioreg）三平台 GPU 采集，best-effort + 缓存。",
            "val": "训练 / 渲染场景的显卡负载一目了然"
          },
          {
            "title": "自定义拨测",
            "color": "accent",
            "icon": "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8z",
            "desc1": "HTTP 状态码、TCP 端口、Ping 延迟、关键进程存活 —— 四种拨测覆盖所有可用性场景。",
            "desc2": "内置历史曲线，支持框选放大与全屏预览。",
            "val": "服务不可达第一时间发现"
          },
          {
            "title": "交互式趋势图",
            "color": "accent",
            "icon": "M3 3v18h18M7 14l4-4 3 3 5-6",
            "desc1": "Canvas 自绘图表，支持悬停数值、框选缩放、全屏预览，深 / 浅主题自适应。",
            "val": "点一下就能下钻，不用切到 Grafana"
          },
          {
            "title": "主机分组与概览",
            "color": "accent",
            "icon": "M3 3h7v7H3zM14 3h7v7h-7zM14 14h7v7h-7zM3 14h7v7H3z",
            "desc1": "按业务 / 机房自定义分组；概览 KPI 卡片实时显示在线 / 离线 / 严重告警 / 警告数量。",
            "val": "几百台机器的健康状况，一屏掌握"
          },
          {
            "title": "业务接口拨测",
            "color": "accent",
            "icon": "M12 2a10 10 0 1 0 10 10A10 10 0 0 0 12 2zM2 12h20M12 2a15 15 0 0 1 0 20 15 15 0 0 1 0-20z",
            "desc1": "对业务 API（HTTP / gRPC / 自定义）发起拨测，校验状态码、延迟与响应内容是否符合预期。",
            "val": "业务挂了，比别人先知道"
          },
          {
            "title": "多维断言",
            "color": "accent",
            "icon": "M9 12l2 2 4-4",
            "desc1": "支持状态码、响应耗时、关键字 / JSON 字段断言，覆盖协议正确性与业务语义双重校验。",
            "val": "不止能 ping 通，还要答得对"
          }
        ]
      },
      {
        "tag": "02",
        "title": "告警治理",
        "desc": "把噪音压下去，让真正严重的故障浮上来",
        "pain": "告警风暴里，真正的故障被淹没、值班人麻木？",
        "roles": [
          "运维",
          "SRE",
          "值班"
        ],
        "items": [
          {
            "title": "多云多渠道告警",
            "color": "warn",
            "icon": "M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9",
            "desc1": "CPU / 内存 / 磁盘 / 负载 / GPU / 离线 等阈值告警；飞书 Webhook、钉钉 Webhook（HMAC 签名）、SMTP 邮件，以及 阿里云 / 华为云 / 腾讯云 三云短信与语音电话（TTS 语音通知）多渠道推送。",
            "desc2": "切换云厂商只需改一处 provider 配置，无需改动部署；号码自动补 +86 前缀，模板参数可自定义。",
            "val": "关键告警一个电话打醒值班人"
          },
          {
            "title": "27 维阈值自定义",
            "color": "warn",
            "icon": "M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6",
            "desc1": "27 组 warn / crit 细粒度阈值，覆盖主机资源、拨测、API 业务、编排任务、端口转发五大维度，逐项可调、保存即生效。",
            "desc2": "主机维度内置保守 / 标准 / 宽松三档预设；留空自动回退推荐默认，填多少用多少，绝不误配漏配。",
            "val": "每类监控都能按自己的标准告警"
          },
          {
            "title": "分级与降噪",
            "color": "warn",
            "icon": "M3 12h4l3-9 4 18 3-9h4",
            "desc1": "严重 / 警告两级，事件去重冷却（5 分钟内相同事件不重复推送），结合噪音抑制，告警量降低 80%。",
            "val": "真正的故障不再被淹没"
          },
          {
            "title": "离线即告警",
            "color": "warn",
            "icon": "M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z",
            "desc1": "Agent 30 秒无上报即触发严重离线告警，分布式环境下的主机失联无所遁形。",
            "val": "机器挂了，你比同事先知道"
          },
          {
            "title": "告警静默",
            "color": "warn",
            "icon": "M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9",
            "desc1": "维护窗口或已知事件期间一键静默，避免重复打扰；静默期结束后自动恢复。",
            "val": "维护期不再被无效告警轰炸"
          },
          {
            "title": "告警路由",
            "color": "accent",
            "icon": "M17 1l4 4-4 4 M3 11V9a4 4 0 0 1 4-4h14 M7 23l-4-4 4-4 M21 13v2a4 4 0 0 1-4 4H3",
            "desc1": "按标签 / 严重度把告警路由到不同渠道与接收组，业务告警、基础设施告警各得其所。",
            "val": "对的告警，推给对的人"
          }
        ]
      },
      {
        "tag": "03",
        "title": "远程可达与审计",
        "desc": "免开端口也能触达，事后全程可追溯",
        "pain": "机房网络隔离，出事到不了现场、事后又说不清？",
        "roles": [
          "运维",
          "安全",
          "审计"
        ],
        "items": [
          {
            "title": "远程终端",
            "color": "ok",
            "icon": "M4 17l6-6-6-6M12 19h8",
            "desc1": "浏览器直连主机终端，Agent 反向连接免开入站端口。多标签页、窗口自适应、完整 VT100 仿真（vim/top 全屏可用），移动端虚拟键盘。",
            "val": "不用 VPN + SSH，浏览器直达"
          },
          {
            "title": "终端会话回放",
            "color": "ok",
            "icon": "M23 4v6h-6M1 20v-6h6M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15",
            "desc1": "所有会话全程录制（时间戳帧），1x/2x/4x/8x 倍速回放；实时旁观让多人同时查看活跃会话；列表支持按操作者 / 主机 / IP 三维搜索。",
            "val": "谁做了什么、何时做的，完整可追溯"
          },
          {
            "title": "端口转发（TCP / UDP / HTTP）",
            "color": "ok",
            "icon": "M4 12v8a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-8M16 6l-4-4-4 4M12 2v13",
            "desc1": "通过 Agent 反向隧道把内网 Web / 数据库 / 调试接口映射到本地浏览器，零公网暴露。支持 TCP / UDP / HTTP 三协议单端口转发，以及 TCP / UDP 端口范围批量转发（单批最多 100 个连续端口，同组统一启停删除）。",
            "desc2": "列表 / 卡片双视图，启用 / 禁用 / 复制 / 编辑 / 删除一键完成；转发统计与健康检测接口实时感知异常。",
            "val": "内网服务，随手就能本地访问"
          },
          {
            "title": "操作日志与审计",
            "color": "ok",
            "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8M16 17H8M10 9H8",
            "desc1": "全量操作日志（操作 / 系统 / 插件三类），支持筛选与 CSV 导出；与终端录制、命令审计共同构成完整审计闭环。",
            "val": "合规审计材料，一键导出"
          }
        ]
      },
      {
        "tag": "04",
        "title": "自动化与故障自愈",
        "desc": "把重复劳动交给剧本，常见故障自动闭环",
        "pain": "同样的故障半夜重复处理，人工剧本又慢又易错？",
        "roles": [
          "运维",
          "SRE"
        ],
        "items": [
          {
            "title": "自动化剧本编排",
            "color": "purple",
            "icon": "M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z",
            "desc1": "可视化定义命令序列，一键批量执行到多台主机；执行结果实时回传，支持成功 / 失败统计与步骤级输出。",
            "desc2": "执行历史完整保留，操作者、时间、结果全部可追溯。",
            "val": "100 台主机批量打补丁，10 分钟完成"
          },
          {
            "title": "告警→剧本自动修复",
            "color": "purple",
            "icon": "M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z",
            "desc1": "规则匹配后自动触发修复剧本；冷却窗口 + 每小时限流 + 可选人工审批三重护栏，防止抖动告警把修复打成雪崩。",
            "desc2": "每次自动修复的规则、剧本、结果全部留痕，可审计可回溯。",
            "val": "常见故障自愈，人只处理真正棘手的"
          },
          {
            "title": "SLO 与错误预算",
            "color": "accent",
            "icon": "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8z",
            "desc1": "基于指标或拨测定义可用性目标；长时间窗口的 SLI 直接从 VictoriaMetrics 计算，错误预算燃尽自动开事件预警。",
            "val": "用数据说话，而不是拍脑袋定 SLA"
          },
          {
            "title": "工单流转",
            "color": "ok",
            "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8M16 17H8M10 9H8",
            "desc1": "事件一键升级为工单，状态流转 + 评论 + 指派全程留痕，处置责任到人。",
            "val": "从发现到闭环，全流程可追踪"
          },
          {
            "title": "事件闭环",
            "color": "ok",
            "icon": "M23 4v6h-6M1 20v-6h6M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15",
            "desc1": "告警 / SLO 燃尽 / 手动都汇聚为事件，带完整时间线（触发→确认→修复→恢复）；同一根因跨主机去重合并，不再满屏重复告警。",
            "val": "故障有主线，不再各看各的告警"
          }
        ]
      },
      {
        "tag": "05",
        "title": "日志与洞察",
        "desc": "指标红了，日志告诉你为什么红",
        "pain": "指标异常，却找不到根因、只能靠猜？",
        "roles": [
          "运维",
          "SRE",
          "开发"
        ],
        "items": [
          {
            "title": "Agent 增量采集",
            "color": "accent",
            "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6",
            "desc1": "--log-paths 指定要采集的日志文件，从文件尾增量跟踪、批量压缩上报，重启续传不重复不丢点。",
            "val": "应用日志自动汇聚，无需登机器 tail"
          },
          {
            "title": "全文检索",
            "color": "accent",
            "icon": "M21 21l-5.2-5.2M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0z",
            "desc1": "按主机 / 级别（error·warn·info）/ 关键字 / 时间范围组合检索，日志自动分级着色，命中即定位。",
            "val": "几十台机器的日志，一个框搜到底"
          },
          {
            "title": "与事件联动",
            "color": "ok",
            "icon": "M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71",
            "desc1": "事件详情自动关联该主机近 1 小时的错误 / 告警日志，指标异常与日志现场并排看，排障不用来回切工具。",
            "val": "指标告诉你哪儿不对，日志告诉你为什么"
          },
          {
            "title": "轻量 AI 异常检测",
            "color": "purple",
            "icon": "M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z",
            "desc1": "插件内置 z-score 轻量异常检测，无需额外机器学习平台即可发现指标突变。",
            "val": "异常自动标红，不用盯图表"
          }
        ]
      },
      {
        "tag": "06",
        "title": "AI 运维助手",
        "desc": "巡检、诊断、自主智能体，让经验沉淀成自动研判",
        "pain": "排障靠老人经验，新人接不住、深夜无人盯屏？",
        "roles": [
          "运维",
          "SRE",
          "管理者"
        ],
        "items": [
          {
            "title": "定时 AI 巡检",
            "color": "purple",
            "icon": "M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z",
            "desc1": "周期性健康巡检，自动汇总在线率、firing 告警、SLO 超标、资源高位与错误日志激增；只在发现风险时推消息，健康时不打扰。",
            "val": "没人盯屏的深夜，也有 AI 在巡检"
          },
          {
            "title": "事件根因诊断",
            "color": "purple",
            "icon": "M22 12h-4l-3 9L9 3l-3 9H2",
            "desc1": "对事件一键做根因研判，输出按可能性排序的根因假设 + 可执行处置步骤，结果自动写入事件时间线；新建严重事件还会自动触发诊断。",
            "val": "排障从「靠经验」变「有抓手」"
          },
          {
            "title": "智能体 + 启发式兜底",
            "color": "accent",
            "icon": "M9 3v2M15 3v2M9 19v2M15 19v2M3 9h2M3 15h2M19 9h2M19 15h2M9 9h6v6H9z",
            "desc1": "配置 AI Provider（LLM）时走智能体级分析；未配置时启发式规则兜底，永不空转。错误 / 告警日志自动纳入分析上下文，判断更贴近现场。",
            "val": "有大模型更聪明，没有也能用"
          },
          {
            "title": "自主智能体",
            "color": "purple",
            "icon": "M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z",
            "desc1": "内置自主 Agent 框架（非外部网关），通过 Function Calling 调用诊断 / 查询 / 修复能力，从「问答」升级为「能动手的运维助手」。",
            "val": "不止给建议，还能执行处置"
          },
          {
            "title": "RAG 诊断知识库",
            "color": "accent",
            "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6",
            "desc1": "基于 pgvector 沉淀历史告警 / 事件 / 日志为可检索诊断向量库，根因研判与巡检结论更贴近真实故障现场。",
            "val": "越用越懂你的系统"
          },
          {
            "title": "向量模型自由配置",
            "color": "accent",
            "icon": "M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20z M2 12h20 M12 2a15 15 0 0 1 0 20 M12 2a15 15 0 0 0 0 20",
            "desc1": "向量化（embedding）模型与对话模型解耦，可指向任意 OpenAI 兼容 /embeddings —— OpenAI、阿里百炼，或自建 bge / m3e / gte 本地模型，端点 / 密钥 / 模型 / 维度独立配置。",
            "desc2": "对话用大模型、向量用轻量模型，各自计费限流；一键测试连通性并回显实际维度，配置错位当场发现。",
            "val": "RAG 不锁厂商，本地私有化也能跑"
          }
        ]
      },
      {
        "tag": "07",
        "title": "安全合规与高可用",
        "desc": "私有化部署，安全、合规、可靠一个不落",
        "pain": "数据出不了内网，安全与合规谁兜底、生产敢不敢上？",
        "roles": [
          "安全",
          "管理者",
          "运维"
        ],
        "items": [
          {
            "title": "多用户 RBAC",
            "color": "purple",
            "icon": "M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2 M9 7a4 4 0 1 0 0 8 4 4 0 0 0 0-8z M23 21v-2a4 4 0 0 0-3-3.87",
            "desc1": "三级角色：管理员（全部操作）、操作员（终端+告警）、观察员（只读）。路由级权限拦截 + 用户管理界面。",
            "val": "不同人看到不同的能力边界"
          },
          {
            "title": "MFA 两步验证",
            "color": "purple",
            "icon": "M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5",
            "desc1": "TOTP 两步验证（RFC 6238，兼容 Google Authenticator），登录与敏感操作二次确认。",
            "val": "账密泄露也进不来"
          },
          {
            "title": "账户找回",
            "color": "purple",
            "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6",
            "desc1": "支持忘记用户名 / 忘记密码（邮箱验证码）/ 邮箱解除 MFA，全程防枚举保护。",
            "val": "管理员离职也不怕锁死"
          },
          {
            "title": "机器指纹鉴权",
            "color": "purple",
            "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4",
            "desc1": "machine-id + MAC 哈希指纹绑定，Agent 终端通道按指纹鉴权（非 Token）。Token 轮换不影响已装 Agent，7 天宽限期。",
            "val": "Token 轮换不中断已部署 Agent"
          },
          {
            "title": "合规审计闭环",
            "color": "purple",
            "icon": "M9 2h6a2 2 0 0 1 2 2v0h3a2 2 0 0 1 2 2v13a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h3a2 2 0 0 1 2-2z M9 14l2 2 4-4",
            "desc1": "终端录制回放 + 操作日志 + MFA + RBAC，契合等保审计对可追溯、可管控、有记录的要求。",
            "val": "合规审计的有力支撑"
          },
          {
            "title": "数据永久存储",
            "color": "accent",
            "icon": "M3 3v18h18M3 13l4-4 3 3 5-6",
            "desc1": "5 秒级原始指标永久保留，不自动过期、不自动删除；高压缩存储，长期回溯无压力。",
            "val": "随时回溯任意历史时刻"
          },
          {
            "title": "多服务端推送与跨机房容灾",
            "color": "ok",
            "icon": "M12 2L2 7v10l10 5 10-5V7L12 2z",
            "desc1": "单 Agent 同时向多个服务端广播，各端独立鉴权重试，适合容灾或跨团队共享监控。",
            "val": "一套采集，多份保障"
          }
        ]
      },
      {
        "tag": "08",
        "title": "极简部署与开放生态",
        "desc": "一个二进制跑起来，还能接住你已有的工具链",
        "pain": "部署一套监控要好几个组件、好几天，还替换不了现有链路？",
        "roles": [
          "运维",
          "管理者",
          "开发"
        ],
        "items": [
          {
            "title": "单二进制 · PG+VM 存储",
            "color": "accent",
            "icon": "M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z",
            "desc1": "服务端 / Agent 均为单个 Go 二进制、零第三方依赖；时序数据统一入 VictoriaMetrics、关系数据入 PostgreSQL，docker compose 一键起全套。",
            "val": "服务端 + PG + VM，compose 一键拉起"
          },
          {
            "title": "安装向导",
            "color": "accent",
            "icon": "M12 3v12M7 10l5 5 5-5 M5 21h14",
            "desc1": "下载 docker-compose.yml 一条命令启动服务端（密钥自动生成写入）；install.sh 自动检测 CPU 架构（AMD64/ARM64）并下载对应 Agent 二进制，一条 curl 完成安装。",
            "val": "运维小白也能 3 分钟上线"
          },
          {
            "title": "网关中继模式",
            "color": "accent",
            "icon": "M17 1l4 4-4 4 M3 11V9a4 4 0 0 1 4-4h14 M7 23l-4-4 4-4 M21 13v2a4 4 0 0 1-4 4H3",
            "desc1": "内网仅一台联网机器代理所有请求到云端，二进制 / 上报 / 终端自动穿透，适合跨网段或防火墙后主机。",
            "val": "无需每台机器都开外网"
          },
          {
            "title": "PWA 离线访问",
            "color": "accent",
            "icon": "M12 18h.01M8 21h8a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2H8a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2z M9 7h.01",
            "desc1": "可安装到桌面（PWA），独立窗口运行；App Shell 离线缓存，断网仍看最后已知状态。",
            "val": "手机也能装，随时随地查看"
          },
          {
            "title": "Python 插件 SDK",
            "color": "purple",
            "icon": "M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4",
            "desc1": "内置插件 SDK，几行代码采集自定义指标（MySQL 连接数、Nginx 请求量、Redis 内存等）。",
            "desc2": "内置示例：进程监控、服务端口探活等。",
            "val": "监控什么你说了算"
          },
          {
            "title": "事件 Webhook 外发",
            "color": "accent",
            "icon": "M22 2L11 13M22 2l-7 20-4-9-9-4 20-7z",
            "desc1": "告警与事件可推送到你的工单系统、IM 或自研平台，打通既有运维流程，不必困在单一界面。",
            "val": "告警直接进入你已有的工单流"
          },
          {
            "title": "飞书 / 钉钉 / 邮件 原生集成",
            "color": "warn",
            "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6",
            "desc1": "五种通知渠道开箱即用（飞书 / 钉钉 / 邮件 / 短信 / 语音电话），钉钉 Webhook 带 HMAC 签名校验，投递既方便又安全。",
            "val": "团队常用的协作工具直接收告警"
          },
          {
            "title": "可观测数据开放",
            "color": "accent",
            "icon": "M3 3v18h18M7 14l4-4 3 3 5-6",
            "desc1": "时序数据基于 VictoriaMetrics，兼容 Prometheus 远程读写协议，可与既有看板 / 告警链路并存。",
            "val": "不替换技术栈，只补上短板"
          }
        ]
      },
      {
        "tag": "09",
        "title": "硬件巡检与存储采集",
        "desc": "从服务器硬件到存储阵列，全栈资产与状态可见",
        "pain": "服务器硬件资产靠人工登记，存储阵列告警靠厂商工具，和主机监控完全割裂？",
        "roles": ["运维", "IT 资产管理", "存储管理员"],
        "items": [
          {
            "title": "Redfish 硬件巡检",
            "color": "accent",
            "icon": "M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4",
            "desc1": "标准 Redfish/DMTF 协议远程采集服务器硬件资产：处理器 / 内存 / 磁盘 / RAID / 网卡 / 风扇 / 电源 / 温度，无需安装 Agent。",
            "desc2": "支持华为 iBMC 深度兼容（ProcessorView / MemoryView 一次性采集），适配 TaiShan / Kunpeng 系列。",
            "val": "硬件资产自动发现，告别手工登记"
          },
          {
            "title": "OceanStor 存储采集",
            "color": "accent",
            "icon": "M2 20h20M5 20V10l7-6 7 6v10M9 20v-4h6v4",
            "desc1": "通过华为 OceanStor RESTful API 采集存储池 / LUN / 控制器 / 磁盘 / 告警等资产与性能数据，纳入统一监控面板。",
            "val": "存储阵列与主机资产同一面板查看"
          },
          {
            "title": "NetFlow 流量分析",
            "color": "accent",
            "icon": "M3 12h4l3-9 4 18 3-9h4",
            "desc1": "内置 NetFlow v5/v9/IPFIX 采集器，五元组（源/目的 IP + 端口 + 协议）流量统计与 TOP-N 排行，可视化流量热力图。",
            "desc2": "flow_records 按日自动分区管理，历史分区可滚动归档，避免单表膨胀。",
            "val": "异常流量一眼定位，带宽滥用无处遁形"
          },
          {
            "title": "硬件资产统一导出",
            "color": "purple",
            "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8 M16 17H8 M10 9H8",
            "desc1": "硬件巡检结果支持导出为 Markdown / Excel / Word / PDF 四种格式，零第三方依赖，方便资产审计与汇报。",
            "val": "巡检报告一键导出，资产审计无忧"
          }
        ]
      },
      {
        "tag": "10",
        "title": "移动运维",
        "desc": "手机上的运维中心，随时随地掌控全局",
        "pain": "不在电脑前就看不到告警、查不了主机、登不上终端？",
        "roles": ["运维", "值班", "管理者"],
        "items": [
          {
            "title": "原生 Android App",
            "color": "ok",
            "icon": "M7 2h10a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2z M12 18h.01",
            "desc1": "Kotlin + Jetpack Compose 原生开发，非 WebView 套壳。Material 3 设计语言，深色/浅色主题自适应。",
            "desc2": "主机总览 / 告警推送 / 远程终端 / 硬件报表，多主机流畅切换。",
            "val": "原生流畅体验，手机也是运维利器"
          },
          {
            "title": "PWA 移动适配",
            "color": "ok",
            "icon": "M12 18h.01M8 21h8a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2H8a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2z M9 7h.01",
            "desc1": "Web 面板可安装到手机桌面（PWA），独立窗口运行；App Shell 离线缓存，断网仍看最后已知状态。",
            "val": "不装 App 也能用，浏览器即入口"
          },
          {
            "title": "私有化自托管",
            "color": "accent",
            "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z",
            "desc1": "自定义服务器地址，数据始终留在内网，不经过任何第三方服务。登录凭据与 Web 端共用同一套 RBAC 账户体系。",
            "val": "数据不出内网，安全合规无忧"
          }
        ]
      }
    ],
    "cta.title": "这些功能，一个二进制全部包含",
    "cta.desc": "不需要 Prometheus + Grafana + Alertmanager + 堡垒机 + 工单系统 —— AIOps 一个搞定",
    "cta.btn2": "查看解决方案"
  },
  "zh-TW": {
    "page.title": "功能詳情 — AIOps",
        "page.desc": "不用再拼 Zabbix + Prometheus + Grafana。AIOps 把監控、告警、遠端終端、自動化自愈、AI 診斷與 SRE 閉環，按你真實的運維痛點組織成一個平台 —— 缺什麼能力，一找就到。",
    "page.oglocale": "zh_TW",
    "head.tag": "功能詳情",
    "head.title": "你需要的功能，按能解決的問題排好了",
    "head.desc": "不是功能清單，而是針對運維真實痛點的能力矩陣",
    "band.tag": "為運維真實痛點而生",
    "band.title": "這些功能，對應你每天都在頭疼的事",
    "pains": [
      {
        "icon": "M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9",
        "t": "告警風暴",
        "d": "每天幾百條告警，真正嚴重的反而被淹沒"
      },
      {
        "icon": "M4 17l6-6-6-6M12 19h8",
        "t": "到不了現場",
        "d": "機房隔離、沒開端口，出事只能乾著急"
      },
      {
        "icon": "M21 21l-5.2-5.2M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0z",
        "t": "找不到根因",
        "d": "指標紅了，卻不知道為什麼紅"
      },
      {
        "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4",
        "t": "不敢上生產",
        "d": "私有化部署，安全合規與可靠性誰兜底"
      }
    ],
    "groups": [
      {
        "tag": "01",
        "title": "全棧可觀測",
        "desc": "從作業系統到業務介面的端到端可視性",
        "pain": "系統到底健不健康，還要一台台 SSH 敲 top/free/df？",
        "roles": [
          "運維",
          "SRE",
          "開發者"
        ],
        "items": [
          {
            "title": "即時指標監控",
            "color": "accent",
            "icon": "M22 12h-4l-3 9L9 3l-3 9H2",
            "desc1": "CPU / 記憶體 / SWAP / 多磁碟 / 網路收發 / 系統負載 / 進程數 / TCP 連接數 / 運行時間 —— 5 秒級採集，全面覆蓋。",
            "desc2": "指標永久儲存，重啟後續傳不丟點。",
            "val": "告別逐台 SSH top/free/df 查看指標"
          },
          {
            "title": "GPU 監控",
            "color": "accent",
            "icon": "M9 3v2M15 3v2M9 19v2M15 19v2M3 9h2M3 15h2M19 9h2M19 15h2M9 9h6v6H9z",
            "desc1": "NVIDIA（nvidia-smi）、AMD（Linux sysfs）、Apple（macOS ioreg）三平台 GPU 採集，best-effort + 快取。",
            "val": "訓練 / 渲染場景的顯卡負載一目了然"
          },
          {
            "title": "自定義撥測",
            "color": "accent",
            "icon": "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8z",
            "desc1": "HTTP 狀態碼、TCP 端口、Ping 延遲、關鍵進程存活 —— 四種撥測覆蓋所有可用性場景。",
            "desc2": "內建歷史曲線，支持框選放大與全螢幕預覽。",
            "val": "服務不可達第一時間發現"
          },
          {
            "title": "互動式趨勢圖",
            "color": "accent",
            "icon": "M3 3v18h18M7 14l4-4 3 3 5-6",
            "desc1": "Canvas 自繪圖表，支持懸停數值、框選縮放、全螢幕預覽，深 / 淺主題自適應。",
            "val": "點一下就能下鑽，不用切到 Grafana"
          },
          {
            "title": "主機分組與概覽",
            "color": "accent",
            "icon": "M3 3h7v7H3zM14 3h7v7h-7zM14 14h7v7h-7zM3 14h7v7H3z",
            "desc1": "按業務 / 機房自定義分組；概覽 KPI 卡片即時顯示在線 / 離線 / 嚴重告警 / 警告數量。",
            "val": "幾百台機器的健康狀況，一屏掌握"
          },
          {
            "title": "業務介面撥測",
            "color": "accent",
            "icon": "M12 2a10 10 0 1 0 10 10A10 10 0 0 0 12 2zM2 12h20M12 2a15 15 0 0 1 0 20 15 15 0 0 1 0-20z",
            "desc1": "對業務 API（HTTP / gRPC / 自定義）發起撥測，校驗狀態碼、延遲與回應內容是否符合預期。",
            "val": "業務掛了，比別人先知道"
          },
          {
            "title": "多維斷言",
            "color": "accent",
            "icon": "M9 12l2 2 4-4",
            "desc1": "支援狀態碼、回應耗時、關鍵字 / JSON 欄位斷言，覆蓋協議正確性與業務語意雙重校驗。",
            "val": "不只 ping 得通，還要答得對"
          }
        ]
      },
      {
        "tag": "02",
        "title": "告警治理",
        "desc": "把噪音壓下去，讓真正嚴重的故障浮上來",
        "pain": "告警風暴裡，真正的故障被淹沒、值班人麻木？",
        "roles": [
          "運維",
          "SRE",
          "值班"
        ],
        "items": [
          {
            "title": "多雲多渠道告警",
            "color": "warn",
            "icon": "M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9",
            "desc1": "CPU / 記憶體 / 磁碟 / 負載 / GPU / 離線 等閾值告警；飛書 Webhook、釘釘 Webhook（HMAC 簽名）、SMTP 郵件，以及 阿里雲 / 華為雲 / 騰訊雲 三雲簡訊與語音電話（TTS 語音通知）多渠道推送。",
            "desc2": "切換雲廠商只需改一處 provider 配置，無需改動部署；號碼自動補 +86 前綴，模板參數可自定義。",
            "val": "關鍵告警一個電話打醒值班人"
          },
          {
            "title": "27 維閾值自定義",
            "color": "warn",
            "icon": "M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6",
            "desc1": "27 組 warn / crit 細粒度閾值，覆蓋主機資源、撥測、API 業務、編排任務、端口轉發五大維度，逐項可調、儲存即生效。",
            "desc2": "主機維度內建保守 / 標準 / 寬鬆三檔預設；留空自動回退推薦預設，填多少用多少，絕不誤配漏配。",
            "val": "每類監控都能按自己的標準告警"
          },
          {
            "title": "分級與降噪",
            "color": "warn",
            "icon": "M3 12h4l3-9 4 18 3-9h4",
            "desc1": "嚴重 / 警告兩級，事件去重冷卻（5 分鐘內相同事件不重複推送），結合噪音抑制，告警量降低 80%。",
            "val": "真正的故障不再被淹沒"
          },
          {
            "title": "離線即告警",
            "color": "warn",
            "icon": "M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z",
            "desc1": "Agent 30 秒無上報即觸發嚴重離線告警，分散式環境下的主機失聯無所遁形。",
            "val": "機器掛了，你比同事先知道"
          },
          {
            "title": "告警靜默",
            "color": "warn",
            "icon": "M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9",
            "desc1": "維護窗口或已知事件期間一鍵靜默，避免重複打擾；靜默期結束後自動恢復。",
            "val": "維護期不再被無效告警轟炸"
          },
          {
            "title": "告警路由",
            "color": "accent",
            "icon": "M17 1l4 4-4 4 M3 11V9a4 4 0 0 1 4-4h14 M7 23l-4-4 4-4 M21 13v2a4 4 0 0 1-4 4H3",
            "desc1": "按標籤 / 嚴重度把告警路由到不同管道與接收組，業務告警、基礎設施告警各得其所。",
            "val": "對的告警，推給對的人"
          }
        ]
      },
      {
        "tag": "03",
        "title": "遠程可達與審計",
        "desc": "免開端口也能觸達，事後全程可追溯",
        "pain": "機房網路隔離，出事到不了現場、事後又說不清？",
        "roles": [
          "運維",
          "安全",
          "審計"
        ],
        "items": [
          {
            "title": "遠程終端",
            "color": "ok",
            "icon": "M4 17l6-6-6-6M12 19h8",
            "desc1": "瀏覽器直連主機終端，Agent 反向連接免開入站端口。多分頁、視窗自適應、完整 VT100 模擬（vim/top 全螢幕可用），行動端虛擬鍵盤。",
            "val": "不用 VPN + SSH，瀏覽器直達"
          },
          {
            "title": "終端會話回放",
            "color": "ok",
            "icon": "M23 4v6h-6M1 20v-6h6M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15",
            "desc1": "所有會話全程錄製（時間戳幀），1x/2x/4x/8x 倍速回放；即時旁觀讓多人同時查看活躍會話；列表支持按操作者 / 主機 / IP 三維搜索。",
            "val": "誰做了什麼、何時做的，完整可追溯"
          },
          {
            "title": "端口轉發（TCP / UDP / HTTP）",
            "color": "ok",
            "icon": "M4 12v8a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-8M16 6l-4-4-4 4M12 2v13",
            "desc1": "透過 Agent 反向隧道把內網 Web / 資料庫 / 除錯介面映射到本地瀏覽器，零公網暴露。支援 TCP / UDP / HTTP 三協定單端口轉發，以及 TCP / UDP 端口範圍批量轉發（單批最多 100 個連續端口，同組統一啟停刪除）。",
            "desc2": "列表 / 卡片雙視圖，啟用 / 停用 / 複製 / 編輯 / 刪除一鍵完成；轉發統計與健康檢測接口即時感知異常。",
            "val": "內網服務，隨手就能本地訪問"
          },
          {
            "title": "操作日誌與審計",
            "color": "ok",
            "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8M16 17H8M10 9H8",
            "desc1": "全量操作日誌（操作 / 系統 / 插件三類），支持篩選與 CSV 匯出；與終端錄製、命令審計共同構成完整審計閉環。",
            "val": "合規審計材料，一鍵匯出"
          }
        ]
      },
      {
        "tag": "04",
        "title": "自動化與故障自愈",
        "desc": "把重複勞動交給劇本，常見故障自動閉環",
        "pain": "同樣的故障半夜重複處理，人工劇本又慢又易錯？",
        "roles": [
          "運維",
          "SRE"
        ],
        "items": [
          {
            "title": "自動化劇本編排",
            "color": "purple",
            "icon": "M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z",
            "desc1": "可視化定義命令序列，一鍵批量執行到多台主機；執行結果即時回傳，支持成功 / 失敗統計與步驟級輸出。",
            "desc2": "執行歷史完整保留，操作者、時間、結果全部可追溯。",
            "val": "100 台主機批量打補丁，10 分鐘完成"
          },
          {
            "title": "告警→劇本自動修復",
            "color": "purple",
            "icon": "M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z",
            "desc1": "規則匹配後自動觸發修復劇本；冷卻窗口 + 每小時限流 + 可選人工審批三重護欄，防止抖動告警把修復打成雪崩。",
            "desc2": "每次自動修復的規則、劇本、結果全部留痕，可審計可回溯。",
            "val": "常見故障自癒，人只處理真正棘手的"
          },
          {
            "title": "SLO 與錯誤預算",
            "color": "accent",
            "icon": "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8z",
            "desc1": "基於指標或撥測定義可用性目標；長時間窗口的 SLI 直接從 VictoriaMetrics 計算，錯誤預算燃盡自動開事件預警。",
            "val": "用數據說話，而不是拍腦袋定 SLA"
          },
          {
            "title": "工單流轉",
            "color": "ok",
            "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8M16 17H8M10 9H8",
            "desc1": "事件一鍵升級為工單，狀態流轉 + 評論 + 指派全程留痕，處置責任到人。",
            "val": "從發現到閉環，全流程可追蹤"
          },
          {
            "title": "事件閉環",
            "color": "ok",
            "icon": "M23 4v6h-6M1 20v-6h6M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15",
            "desc1": "告警 / SLO 燃盡 / 手動都匯聚為事件，帶完整時間線（觸發→確認→修復→恢復）；同一根因跨主機去重合併，不再滿屏重複告警。",
            "val": "故障有主線，不再各看各的告警"
          }
        ]
      },
      {
        "tag": "05",
        "title": "日誌與洞察",
        "desc": "指標紅了，日誌告訴你為什麼紅",
        "pain": "指標異常，卻找不到根因、只能靠猜？",
        "roles": [
          "運維",
          "SRE",
          "開發"
        ],
        "items": [
          {
            "title": "Agent 增量採集",
            "color": "accent",
            "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6",
            "desc1": "--log-paths 指定要採集的日誌文件，從文件尾增量跟蹤、批量壓縮上報，重啟續傳不重複不丟點。",
            "val": "應用日誌自動匯聚，無需登機器 tail"
          },
          {
            "title": "全文檢索",
            "color": "accent",
            "icon": "M21 21l-5.2-5.2M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0z",
            "desc1": "按主機 / 級別（error·warn·info）/ 關鍵字 / 時間範圍組合檢索，日誌自動分級著色，命中即定位。",
            "val": "幾十台機器的日誌，一個框搜到底"
          },
          {
            "title": "與事件聯動",
            "color": "ok",
            "icon": "M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71",
            "desc1": "事件詳情自動關聯該主機近 1 小時的錯誤 / 告警日誌，指標異常與日誌現場並排看，排障不用來回切工具。",
            "val": "指標告訴你哪兒不對，日誌告訴你為什麼"
          },
          {
            "title": "輕量 AI 異常檢測",
            "color": "purple",
            "icon": "M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z",
            "desc1": "插件內建 z-score 輕量異常檢測，無需額外機器學習平台即可發現指標突變。",
            "val": "異常自動標紅，不用盯圖表"
          }
        ]
      },
      {
        "tag": "06",
        "title": "AI 運維助手",
        "desc": "巡檢、診斷、自主智能體，讓經驗沉澱成自動研判",
        "pain": "排障靠老人經驗，新人接不住、深夜無人盯屏？",
        "roles": [
          "運維",
          "SRE",
          "管理者"
        ],
        "items": [
          {
            "title": "定時 AI 巡檢",
            "color": "purple",
            "icon": "M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z",
            "desc1": "週期性健康巡檢，自動匯總在線率、firing 告警、SLO 超標、資源高位與錯誤日誌激增；只在發現風險時推消息，健康時不打擾。",
            "val": "沒人盯屏的深夜，也有 AI 在巡檢"
          },
          {
            "title": "事件根因診斷",
            "color": "purple",
            "icon": "M22 12h-4l-3 9L9 3l-3 9H2",
            "desc1": "對事件一鍵做根因研判，輸出按可能性排序的根因假設 + 可執行處置步驟，結果自動寫入事件時間線；新建嚴重事件還會自動觸發診斷。",
            "val": "排障從「靠經驗」變「有抓手」"
          },
          {
            "title": "智能體 + 啟發式兜底",
            "color": "accent",
            "icon": "M9 3v2M15 3v2M9 19v2M15 19v2M3 9h2M3 15h2M19 9h2M19 15h2M9 9h6v6H9z",
            "desc1": "配置 AI Provider（LLM）時走智能體級分析；未配置時啟發式規則兜底，永不空轉。錯誤 / 告警日誌自動納入分析上下文，判斷更貼近現場。",
            "val": "有大模型更聰明，沒有也能用"
          },
          {
            "title": "自主智能體",
            "color": "purple",
            "icon": "M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z",
            "desc1": "內建自主 Agent 框架（非外部閘道），透過 Function Calling 呼叫診斷 / 查詢 / 修復能力，從「問答」升級為「能動手的運維助手」。",
            "val": "不只給建議，還能執行處置"
          },
          {
            "title": "RAG 診斷知識庫",
            "color": "accent",
            "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6",
            "desc1": "基於 pgvector 將歷史告警 / 事件 / 日誌沉澱為可檢索診斷向量庫，根因研判與巡檢結論更貼近真實故障現場。",
            "val": "越用越懂你的系統"
          },
          {
            "title": "向量模型自由配置",
            "color": "accent",
            "icon": "M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20z M2 12h20 M12 2a15 15 0 0 1 0 20 M12 2a15 15 0 0 0 0 20",
            "desc1": "向量化（embedding）模型與對話模型解耦，可指向任意 OpenAI 相容 /embeddings —— OpenAI、阿里百煉，或自建 bge / m3e / gte 本地模型，端點 / 密鑰 / 模型 / 維度獨立配置。",
            "desc2": "對話用大模型、向量用輕量模型，各自計費限流；一鍵測試連通性並回顯實際維度，配置錯位當場發現。",
            "val": "RAG 不鎖廠商，本地私有化也能跑"
          }
        ]
      },
      {
        "tag": "07",
        "title": "安全合規與高可用",
        "desc": "私有化部署，安全、合規、可靠一個不落",
        "pain": "資料出不了內網，安全與合規誰兜底、生產敢不敢上？",
        "roles": [
          "安全",
          "管理者",
          "運維"
        ],
        "items": [
          {
            "title": "多使用者 RBAC",
            "color": "purple",
            "icon": "M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2 M9 7a4 4 0 1 0 0 8 4 4 0 0 0 0-8z M23 21v-2a4 4 0 0 0-3-3.87",
            "desc1": "三級角色：管理員（全部操作）、操作員（終端+告警）、觀察員（唯讀）。路由級權限攔截 + 使用者管理介面。",
            "val": "不同人看到不同的能力邊界"
          },
          {
            "title": "MFA 兩步驗證",
            "color": "purple",
            "icon": "M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5",
            "desc1": "TOTP 兩步驗證（RFC 6238，相容 Google Authenticator），登入與敏感操作二次確認。",
            "val": "帳密洩露也進不來"
          },
          {
            "title": "帳號找回",
            "color": "purple",
            "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6",
            "desc1": "支持忘記使用者名 / 忘記密碼（郵箱驗證碼）/ 郵箱解除 MFA，全程防枚舉保護。",
            "val": "管理員離職也不怕鎖死"
          },
          {
            "title": "機器指紋鑑權",
            "color": "purple",
            "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4",
            "desc1": "machine-id + MAC 雜湊指紋綁定，Agent 終端通道按指紋鑑權（非 Token）。Token 輪換不影響已裝 Agent，7 天寬限期。",
            "val": "Token 輪換不中斷已部署 Agent"
          },
          {
            "title": "合規審計閉環",
            "color": "purple",
            "icon": "M9 2h6a2 2 0 0 1 2 2v0h3a2 2 0 0 1 2 2v13a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h3a2 2 0 0 1 2-2z M9 14l2 2 4-4",
            "desc1": "終端錄製回放 + 操作日誌 + MFA + RBAC，契合等保審計對可追溯、可管控、有記錄的要求。",
            "val": "合規審計的有力支撐"
          },
          {
            "title": "資料永久儲存",
            "color": "accent",
            "icon": "M3 3v18h18M3 13l4-4 3 3 5-6",
            "desc1": "5 秒級原始指標永久保留，不自動過期、不自動刪除；高壓縮儲存，長期回溯無壓力。",
            "val": "隨時回溯任意歷史時刻"
          },
          {
            "title": "多服務端推送與跨機房容災",
            "color": "ok",
            "icon": "M12 2L2 7v10l10 5 10-5V7L12 2z",
            "desc1": "單 Agent 同時向多個服務端廣播，各端獨立鑑權重試，適合容災或跨團隊共享監控。",
            "val": "一套採集，多份保障"
          }
        ]
      },
      {
        "tag": "08",
        "title": "極簡部署與開放生態",
        "desc": "一個二進制跑起來，還能接住你已有的工具鏈",
        "pain": "部署一套監控要好幾個元件、好幾天，還替換不了現有鏈路？",
        "roles": [
          "運維",
          "管理者",
          "開發"
        ],
        "items": [
          {
            "title": "單二進制 · PG+VM 存儲",
            "color": "accent",
            "icon": "M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z",
            "desc1": "服務端 / Agent 均為單個 Go 二進制、零第三方依賴；時序資料統一入 VictoriaMetrics、關係資料入 PostgreSQL，docker compose 一鍵起全套。",
            "val": "服務端 + PG + VM，compose 一鍵拉起"
          },
          {
            "title": "安裝精靈",
            "color": "accent",
            "icon": "M12 3v12M7 10l5 5 5-5 M5 21h14",
            "desc1": "下載 docker-compose.yml 一條命令啟動服務端（密鑰自動生成寫入）；install.sh 自動檢測 CPU 架構（AMD64/ARM64）並下載對應 Agent 二進制，一條 curl 完成安裝。",
            "val": "運維小白也能 3 分鐘上線"
          },
          {
            "title": "網關中繼模式",
            "color": "accent",
            "icon": "M17 1l4 4-4 4 M3 11V9a4 4 0 0 1 4-4h14 M7 23l-4-4 4-4 M21 13v2a4 4 0 0 1-4 4H3",
            "desc1": "內網僅一台聯網機器代理所有請求到雲端，二進制 / 上報 / 終端自動穿透，適合跨網段或防火牆後主機。",
            "val": "無需每台機器都開外網"
          },
          {
            "title": "PWA 離線訪問",
            "color": "accent",
            "icon": "M12 18h.01M8 21h8a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2H8a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2z M9 7h.01",
            "desc1": "可安裝到桌面（PWA），獨立視窗運行；App Shell 離線快取，斷網仍看最後已知狀態。",
            "val": "手機也能裝，隨時隨地查看"
          },
          {
            "title": "Python 插件 SDK",
            "color": "purple",
            "icon": "M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4",
            "desc1": "內建插件 SDK，幾行程式碼採集自定義指標（MySQL 連接數、Nginx 請求量、Redis 記憶體等）。",
            "desc2": "內建範例：進程監控、服務端口探活等。",
            "val": "監控什麼你說了算"
          },
          {
            "title": "事件 Webhook 外發",
            "color": "accent",
            "icon": "M22 2L11 13M22 2l-7 20-4-9-9-4 20-7z",
            "desc1": "告警與事件可推送到你的工單系統、IM 或自研平台，打通既有運維流程，不必困在單一介面。",
            "val": "告警直接進入你已有的工單流"
          },
          {
            "title": "飛書 / 釘釘 / 郵件 原生整合",
            "color": "warn",
            "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6",
            "desc1": "五種通知管道開箱即用（飛書 / 釘釘 / 郵件 / 簡訊 / 語音電話），釘釘 Webhook 帶 HMAC 簽名校驗，投遞既方便又安全。",
            "val": "團隊常用的協作工具直接收告警"
          },
          {
            "title": "可觀測數據開放",
            "color": "accent",
            "icon": "M3 3v18h18M7 14l4-4 3 3 5-6",
            "desc1": "時序數據基於 VictoriaMetrics，相容 Prometheus 遠程讀寫協議，可與既有看板 / 告警鏈路並存。",
            "val": "不替換技術棧，只補上短板"
          }
        ]
      },
      {
        "tag": "09",
        "title": "硬體巡檢與儲存採集",
        "desc": "從伺服器硬體到儲存陣列，全棧資產與狀態可見",
        "pain": "伺服器硬體資產靠人工登記，儲存陣列告警靠廠商工具，和主機監控完全割裂？",
        "roles": ["運維", "IT 資產管理", "儲存管理員"],
        "items": [
          {
            "title": "Redfish 硬體巡檢",
            "color": "accent",
            "icon": "M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4",
            "desc1": "標準 Redfish/DMTF 協定遠端採集伺服器硬體資產：處理器 / 記憶體 / 磁碟 / RAID / 網卡 / 風扇 / 電源 / 溫度，無需安裝 Agent。",
            "desc2": "支援華為 iBMC 深度相容（ProcessorView / MemoryView 一次性採集），適配 TaiShan / Kunpeng 系列。",
            "val": "硬體資產自動發現，告別手工登記"
          },
          {
            "title": "OceanStor 儲存採集",
            "color": "accent",
            "icon": "M2 20h20M5 20V10l7-6 7 6v10M9 20v-4h6v4",
            "desc1": "透過華為 OceanStor RESTful API 採集儲存池 / LUN / 控制器 / 磁碟 / 告警等資產與效能資料，納入統一監控面板。",
            "val": "儲存陣列與主機資產同一面板查看"
          },
          {
            "title": "NetFlow 流量分析",
            "color": "accent",
            "icon": "M3 12h4l3-9 4 18 3-9h4",
            "desc1": "內建 NetFlow v5/v9/IPFIX 採集器，五元組（源/目的 IP + 端口 + 協定）流量統計與 TOP-N 排行，視覺化流量熱力圖。",
            "desc2": "flow_records 按日自動分區，過期自動清理，避免單表膨脹。",
            "val": "異常流量一眼定位，頻寬濫用無處遁形"
          },
          {
            "title": "硬體資產統一匯出",
            "color": "purple",
            "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8 M16 17H8 M10 9H8",
            "desc1": "硬體巡檢結果支援匯出為 Markdown / Excel / Word / PDF 四種格式，零第三方依賴，方便資產審計與匯報。",
            "val": "巡檢報告一鍵匯出，資產審計無憂"
          }
        ]
      },
      {
        "tag": "10",
        "title": "移動運維",
        "desc": "手機上的運維中心，隨時隨地掌控全局",
        "pain": "不在電腦前就看不到告警、查不了主機、登不上終端？",
        "roles": ["運維", "值班", "管理者"],
        "items": [
          {
            "title": "原生 Android App",
            "color": "ok",
            "icon": "M7 2h10a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2z M12 18h.01",
            "desc1": "Kotlin + Jetpack Compose 原生開發，非 WebView 套殼。Material 3 設計語言，深色/淺色主題自適應。",
            "desc2": "主機總覽 / 告警推送 / 遠端終端 / 硬體報表，多主機流暢切換。",
            "val": "原生流暢體驗，手機也是運維利器"
          },
          {
            "title": "PWA 移動適配",
            "color": "ok",
            "icon": "M12 18h.01M8 21h8a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2H8a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2z M9 7h.01",
            "desc1": "Web 面板可安裝到手機桌面（PWA），獨立視窗執行；App Shell 離線快取，斷網仍看最後已知狀態。",
            "val": "不裝 App 也能用，瀏覽器即入口"
          },
          {
            "title": "私有化自託管",
            "color": "accent",
            "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z",
            "desc1": "自定義伺服器位址，資料始終留在內網，不經過任何第三方服務。登入憑據與 Web 端共用同一套 RBAC 帳戶體系。",
            "val": "資料不出內網，安全合規無憂"
          }
        ]
      }
    ],
    "cta.title": "這些功能，一個二進制全部包含",
    "cta.desc": "不需要 Prometheus + Grafana + Alertmanager + 堡壘機 + 工單系統 —— AIOps 一個搞定",
    "cta.btn2": "查看解決方案"
  },
  "en": {
    "page.title": "Features — AIOps",
    "page.desc": "Tired of stitching Zabbix + Prometheus + Grafana? AIOps organizes monitoring, alerting, remote terminal, automated self-healing, AI diagnosis and the SRE loop into one platform — grouped by the ops pain you actually feel, so the capability you need is one click away.",
    "page.oglocale": "en_US",
    "head.tag": "Features",
    "head.title": "Features, organized by the problem they solve",
    "head.desc": "Not a feature list — a capability map for real ops pain points",
    "band.tag": "Built for Real Ops Pain",
    "band.title": "These features map to the things that keep you up at night",
    "pains": [
      {
        "icon": "M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9",
        "t": "Alert storms",
        "d": "Hundreds of alerts a day; the real one gets buried"
      },
      {
        "icon": "M4 17l6-6-6-6M12 19h8",
        "t": "Can't reach the box",
        "d": "Isolated DC, no open ports — stuck when it breaks"
      },
      {
        "icon": "M21 21l-5.2-5.2M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0z",
        "t": "No root cause",
        "d": "Metric's red but you don't know why"
      },
      {
        "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4",
        "t": "Afraid of production",
        "d": "Self-hosted — who guarantees security, compliance, reliability?"
      }
    ],
    "groups": [
      {
        "tag": "01",
        "title": "Full-Stack Observability",
        "desc": "End-to-end visibility from OS to business APIs",
        "pain": "Still SSH-ing into every host to run top/free/df just to know if it's healthy?",
        "roles": [
          "Ops",
          "SRE",
          "Developer"
        ],
        "items": [
          {
            "title": "Real-time Metrics",
            "color": "accent",
            "icon": "M22 12h-4l-3 9L9 3l-3 9H2",
            "desc1": "CPU, Memory/SWAP, multi-disk, network I/O, load, process count, TCP connections, uptime — 5-second collection, full coverage.",
            "desc2": "Metrics are stored permanently; points resume after restart without gaps.",
            "val": "No more SSH top/free/df on every host"
          },
          {
            "title": "GPU Monitoring",
            "color": "accent",
            "icon": "M9 3v2M15 3v2M9 19v2M15 19v2M3 9h2M3 15h2M19 9h2M19 15h2M9 9h6v6H9z",
            "desc1": "NVIDIA (nvidia-smi), AMD (Linux sysfs), Apple (macOS ioreg) GPU collection across platforms, best-effort + cached.",
            "val": "GPU load for training/rendering at a glance"
          },
          {
            "title": "Custom Health Probes",
            "color": "accent",
            "icon": "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8z",
            "desc1": "HTTP status, TCP port, Ping latency, process liveness — four probe types cover every availability scenario.",
            "desc2": "Built-in history charts with box-select zoom and full-screen preview.",
            "val": "Detect service outages instantly"
          },
          {
            "title": "Interactive Trend Charts",
            "color": "accent",
            "icon": "M3 3v18h18M7 14l4-4 3 3 5-6",
            "desc1": "Canvas-rendered charts with hover values, box-zoom, full-screen preview, and dark/light theme adaptation.",
            "val": "Drill down in one click — no need to switch to Grafana"
          },
          {
            "title": "Host Groups & Overview",
            "color": "accent",
            "icon": "M3 3h7v7H3zM14 3h7v7h-7zM14 14h7v7h-7zM3 14h7v7H3z",
            "desc1": "Group hosts by business/DC; overview KPI cards show online/offline/critical/warning counts in real time.",
            "val": "Health of hundreds of hosts, on one screen"
          },
          {
            "title": "Business Endpoint Probing",
            "color": "accent",
            "icon": "M12 2a10 10 0 1 0 10 10A10 10 0 0 0 12 2zM2 12h20M12 2a15 15 0 0 1 0 20 15 15 0 0 1 0-20z",
            "desc1": "Probe business APIs (HTTP / gRPC / custom) to verify status code, latency and response body against expectations.",
            "val": "Know a business outage before your users do"
          },
          {
            "title": "Multi-Dimension Assertions",
            "color": "accent",
            "icon": "M9 12l2 2 4-4",
            "desc1": "Assert on status code, response time, and keyword / JSON-field checks — covering both protocol correctness and business semantics.",
            "val": "Not just reachable — actually correct"
          }
        ]
      },
      {
        "tag": "02",
        "title": "Alert Governance",
        "desc": "Cut the noise so real incidents surface",
        "pain": "In an alert storm, the real incident gets buried and on-call goes numb?",
        "roles": [
          "Ops",
          "SRE",
          "On-call"
        ],
        "items": [
          {
            "title": "Multi-Cloud, Multi-Channel Alerts",
            "color": "warn",
            "icon": "M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9",
            "desc1": "CPU/Memory/Disk/Load/GPU/Offline threshold alerts; Feishu Webhook, DingTalk (HMAC), SMTP email, plus SMS & voice call (TTS) across Aliyun / Huawei Cloud / Tencent Cloud.",
            "desc2": "Switching cloud vendor is a one-line provider change — no redeploy; numbers auto-prefix +86 and template params are customizable.",
            "val": "A critical alert can literally call and wake on-call"
          },
          {
            "title": "27 Customizable Thresholds",
            "color": "warn",
            "icon": "M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6",
            "desc1": "27 fine-grained warn/crit pairs across host resources, probes, API business, scheduled tasks and port forwarding — each individually tunable, effective on save.",
            "desc2": "Host metrics ship conservative/standard/relaxed presets; blank values auto-fall-back to recommended defaults — never mis-config or leave a gap.",
            "val": "Every monitor type alerts by your own standard"
          },
          {
            "title": "Tiered & De-noised",
            "color": "warn",
            "icon": "M3 12h4l3-9 4 18 3-9h4",
            "desc1": "Critical/warning tiers, event dedup cooldown (no repeat within 5 min), plus noise suppression — 80% less alert volume.",
            "val": "Real incidents no longer buried in noise"
          },
          {
            "title": "Offline = Alert",
            "color": "warn",
            "icon": "M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z",
            "desc1": "30s of no report from an agent triggers a critical offline alert — host loss in distributed setups never goes unnoticed.",
            "val": "You know a machine died before your colleagues do"
          },
          {
            "title": "Alert Silence",
            "color": "warn",
            "icon": "M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9",
            "desc1": "One-click silence during maintenance windows or known incidents to avoid repeat noise; auto-resumes when the window ends.",
            "val": "No meaningless alerts during maintenance"
          },
          {
            "title": "Alert Routing",
            "color": "accent",
            "icon": "M17 1l4 4-4 4 M3 11V9a4 4 0 0 1 4-4h14 M7 23l-4-4 4-4 M21 13v2a4 4 0 0 1-4 4H3",
            "desc1": "Route alerts by label / severity to different channels and recipient groups, so business and infra alerts each reach their owner.",
            "val": "Right alert to the right person"
          }
        ]
      },
      {
        "tag": "03",
        "title": "Remote Access & Audit",
        "desc": "Reachable without open ports, fully traceable after the fact",
        "pain": "Network-isolated DCs — can't reach the box when it breaks, and can't explain what happened after?",
        "roles": [
          "Ops",
          "Security",
          "Audit"
        ],
        "items": [
          {
            "title": "Remote Terminal",
            "color": "ok",
            "icon": "M4 17l6-6-6-6M12 19h8",
            "desc1": "Browser-to-host terminal via agent reverse connection — no inbound ports. Multi-tab, auto-resize, full VT100 (vim/top), mobile keyboard.",
            "val": "No VPN + SSH — straight from the browser"
          },
          {
            "title": "Session Replay",
            "color": "ok",
            "icon": "M23 4v6h-6M1 20v-6h6M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15",
            "desc1": "All sessions recorded (timestamped frames), 1x/2x/4x/8x playback; live observation for multiple viewers; searchable by operator/host/IP.",
            "val": "Full audit trail — who did what, when"
          },
          {
            "title": "Port Forwarding (TCP / UDP / HTTP)",
            "color": "ok",
            "icon": "M4 12v8a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-8M16 6l-4-4-4 4M12 2v13",
            "desc1": "Map internal web apps, databases, and debug endpoints to your local browser via the agent's reverse tunnel — zero public exposure. Single-port forwarding over TCP / UDP / HTTP, plus TCP / UDP port-range batch forwarding (up to 100 contiguous ports per batch, managed as one group).",
            "desc2": "List + card views; enable/disable/copy/edit/delete in one click; stats and health endpoints catch anomalies early.",
            "val": "Internal services, accessible from your laptop in seconds"
          },
          {
            "title": "Operation Logs & Audit",
            "color": "ok",
            "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8M16 17H8M10 9H8",
            "desc1": "Full operation logs (operation/system/plugin), filterable and CSV-exportable; together with session recording and command audit, a complete audit loop.",
            "val": "Compliance audit materials, exported in one click"
          }
        ]
      },
      {
        "tag": "04",
        "title": "Automation & Self-Healing",
        "desc": "Hand repetitive toil to playbooks; common failures close themselves",
        "pain": "Same failure, handled manually at 3am, slow and error-prone every time?",
        "roles": [
          "Ops",
          "SRE"
        ],
        "items": [
          {
            "title": "Automation Playbooks",
            "color": "purple",
            "icon": "M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z",
            "desc1": "Visual command-sequence orchestration, one-click batch execution to multiple hosts; real-time results, success/failure stats, step-level output.",
            "desc2": "Full execution history with operator, timing, and results.",
            "val": "Patch 100 hosts in 10 minutes"
          },
          {
            "title": "Alert→playbook auto-remediation",
            "color": "purple",
            "icon": "M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z",
            "desc1": "A matching rule auto-triggers a remediation playbook, guarded by a cooldown window + hourly rate limit + optional human approval, so a flapping alert can't turn remediation into an avalanche.",
            "desc2": "Every auto-remediation's rule, playbook and result is logged — auditable and traceable.",
            "val": "Common failures self-heal; humans handle the hard ones"
          },
          {
            "title": "SLO & error budget",
            "color": "accent",
            "icon": "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16a4 4 0 1 0 0-8 4 4 0 0 0 0 8z",
            "desc1": "Define availability targets from metrics or probes; long-window SLIs are computed straight from VictoriaMetrics, and a burnt error budget auto-opens an incident.",
            "val": "SLAs backed by data, not gut feel"
          },
          {
            "title": "Ticket workflow",
            "color": "ok",
            "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8M16 17H8M10 9H8",
            "desc1": "Escalate an incident to a ticket in one click; status flow + comments + assignment are all recorded, with clear ownership.",
            "val": "Traceable end to end, from detection to closure"
          },
          {
            "title": "Incident closed loop",
            "color": "ok",
            "icon": "M23 4v6h-6M1 20v-6h6M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15",
            "desc1": "Alerts, SLO burn and manual reports all roll up into incidents with a full timeline (fire→ack→remediate→resolve); the same root cause is deduped across hosts, so no more wall of repeated alerts.",
            "val": "One storyline per incident, not scattered alerts"
          }
        ]
      },
      {
        "tag": "05",
        "title": "Logs & Insight",
        "desc": "Metrics go red — logs tell you why",
        "pain": "Metric anomaly, but no root cause — just guessing?",
        "roles": [
          "Ops",
          "SRE",
          "Dev"
        ],
        "items": [
          {
            "title": "Agent incremental tailing",
            "color": "accent",
            "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6",
            "desc1": "--log-paths names the files to collect; the agent tails from the file end, batches and compresses uploads, and resumes after restart without dupes or gaps.",
            "val": "App logs aggregate automatically — no SSH tail"
          },
          {
            "title": "Full-text search",
            "color": "accent",
            "icon": "M21 21l-5.2-5.2M17 10a7 7 0 1 1-14 0 7 7 0 0 1 14 0z",
            "desc1": "Search by host / level (error·warn·info) / keyword / time range combined; logs are auto-classified and color-coded, so a hit is a location.",
            "val": "Logs from dozens of hosts, one search box"
          },
          {
            "title": "Linked to incidents",
            "color": "ok",
            "icon": "M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71",
            "desc1": "An incident auto-attaches that host's error / warn logs from the last hour, so the metric anomaly and the log scene sit side by side — no tool switching.",
            "val": "Metrics say what's wrong; logs say why"
          },
          {
            "title": "Lightweight AI Anomaly",
            "color": "purple",
            "icon": "M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z",
            "desc1": "Plugin ships z-score lightweight anomaly detection — spot metric spikes without a separate ML platform.",
            "val": "Anomalies auto-flagged; no need to stare at charts"
          }
        ]
      },
      {
        "tag": "06",
        "title": "AI Ops Assistant",
        "desc": "Inspection, diagnosis, autonomous agent — turn experience into automated judgment",
        "pain": "Troubleshooting relies on veteran instinct; juniors can't pick it up and no one watches at 3am?",
        "roles": [
          "Ops",
          "SRE",
          "Manager"
        ],
        "items": [
          {
            "title": "Scheduled AI inspection",
            "color": "purple",
            "icon": "M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z",
            "desc1": "Periodic health inspection auto-summarizes online rate, firing alerts, SLO breaches, resource hotspots and error-log surges; it only pushes a message when it finds risk, staying quiet when healthy.",
            "val": "Even at 3am with no one watching, AI is inspecting"
          },
          {
            "title": "Incident root-cause diagnosis",
            "color": "purple",
            "icon": "M22 12h-4l-3 9L9 3l-3 9H2",
            "desc1": "One click runs a root-cause analysis on an incident, producing likelihood-ranked hypotheses + actionable steps written straight into the timeline; a new critical incident also auto-triggers diagnosis.",
            "val": "Troubleshooting gains a handle, not just intuition"
          },
          {
            "title": "Agent + heuristic fallback",
            "color": "accent",
            "icon": "M9 3v2M15 3v2M9 19v2M15 19v2M3 9h2M3 15h2M19 9h2M19 15h2M9 9h6v6H9z",
            "desc1": "With an AI provider (LLM) configured it does agent-level analysis; without one, heuristic rules take over so it never comes up empty. Error / warn logs are folded into the context for judgments closer to the scene.",
            "val": "Smarter with an LLM, still works without one"
          },
          {
            "title": "Autonomous Agent",
            "color": "purple",
            "icon": "M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z",
            "desc1": "A built-in autonomous agent framework (not an external gateway) that uses Function Calling to invoke diagnosis / query / remediation — upgraded from Q&A to an assistant that can act.",
            "val": "Not just advice — it can act"
          },
          {
            "title": "RAG Diagnosis Knowledge Base",
            "color": "accent",
            "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6",
            "desc1": "Historical alerts / incidents / logs are embedded into a pgvector-backed, retrievable diagnosis knowledge base, so root-cause and inspection conclusions stay close to real incident scenes.",
            "val": "The more it runs, the better it knows your system"
          },
          {
            "title": "Freely Configurable Embedding Model",
            "color": "accent",
            "icon": "M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20z M2 12h20 M12 2a15 15 0 0 1 0 20 M12 2a15 15 0 0 0 0 20",
            "desc1": "The embedding model is decoupled from the chat model and can point to any OpenAI-compatible /embeddings — OpenAI, Aliyun BaiLian, or self-hosted bge / m3e / gte — with independent endpoint / key / model / dimension.",
            "desc2": "Use a large model for chat and a lightweight one for vectors, each billed and rate-limited separately; one-click connectivity test echoes the actual dimension to catch mismatches on the spot.",
            "val": "RAG is vendor-neutral — runs fully on-prem too"
          }
        ]
      },
      {
        "tag": "07",
        "title": "Security, Compliance & HA",
        "desc": "Self-hosted — security, compliance and reliability, all covered",
        "pain": "Data can't leave the intranet; who guarantees security and compliance, and is production safe?",
        "roles": [
          "Security",
          "Manager",
          "Ops"
        ],
        "items": [
          {
            "title": "Multi-User RBAC",
            "color": "purple",
            "icon": "M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2 M9 7a4 4 0 1 0 0 8 4 4 0 0 0 0-8z M23 21v-2a4 4 0 0 0-3-3.87",
            "desc1": "Three-tier roles: admin (all), operator (terminal+alerts), viewer (read-only). Route-level enforcement + user management UI.",
            "val": "Different people see different capability boundaries"
          },
          {
            "title": "MFA Two-Step",
            "color": "purple",
            "icon": "M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5",
            "desc1": "TOTP two-step verification (RFC 6238, Google Authenticator compatible) for login and sensitive actions.",
            "val": "Credential leaks still can't get in"
          },
          {
            "title": "Account Recovery",
            "color": "purple",
            "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6",
            "desc1": "Forgot username / forgot password (email code) / email unbind MFA — all with brute-force/enumeration protection.",
            "val": "An admin leaving doesn't lock you out"
          },
          {
            "title": "Machine Fingerprint Auth",
            "color": "purple",
            "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4",
            "desc1": "machine-id + MAC hash fingerprint binding; terminal channel authenticates by fingerprint, not token. Token rotation doesn't break deployed agents.",
            "val": "Token rotation never breaks deployed agents"
          },
          {
            "title": "Compliance Audit Loop",
            "color": "purple",
            "icon": "M9 2h6a2 2 0 0 1 2 2v0h3a2 2 0 0 1 2 2v13a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h3a2 2 0 0 1 2-2z M9 14l2 2 4-4",
            "desc1": "Session replay + operation logs + MFA + RBAC together meet compliance-audit requirements for traceability, control, and record-keeping.",
            "val": "Strong support for compliance audits"
          },
          {
            "title": "Permanent Data Storage",
            "color": "accent",
            "icon": "M3 3v18h18M3 13l4-4 3 3 5-6",
            "desc1": "5-second raw metrics are kept permanently — never auto-expired or auto-deleted; high-compression storage makes long-range lookback effortless.",
            "val": "Look back to any moment in history"
          },
          {
            "title": "Multi-Server Push & Cross-Site DR",
            "color": "ok",
            "icon": "M12 2L2 7v10l10 5 10-5V7L12 2z",
            "desc1": "One agent broadcasts to multiple servers with independent auth/retry — fit for cross-site DR or cross-team shared monitoring.",
            "val": "One collection, multiple safeguards"
          }
        ]
      },
      {
        "tag": "08",
        "title": "Simple Deploy & Open Ecosystem",
        "desc": "Runs as one binary, and plugs into the toolchain you already have",
        "pain": "Standing up monitoring means several components and days, and still can’t replace your current stack?",
        "roles": [
          "Ops",
          "Manager",
          "Dev"
        ],
        "items": [
          {
            "title": "Single Binary + PG/VM Storage",
            "color": "accent",
            "icon": "M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z",
            "desc1": "Server and agent are each a single Go binary with zero third-party deps; time-series goes to VictoriaMetrics and relational data to PostgreSQL, all brought up by one docker compose command.",
            "val": "Server + PG + VM, up with one compose command"
          },
          {
            "title": "Install Wizard",
            "color": "accent",
            "icon": "M12 3v12M7 10l5 5 5-5 M5 21h14",
            "desc1": "Download docker-compose.yml and one compose command starts the server (secrets auto-generated and written in); install.sh auto-detects CPU arch (AMD64/ARM64) and downloads the matching agent binary — one curl to install.",
            "val": "Even a beginner ships in 3 minutes"
          },
          {
            "title": "Gateway Relay Mode",
            "color": "accent",
            "icon": "M17 1l4 4-4 4 M3 11V9a4 4 0 0 1 4-4h14 M7 23l-4-4 4-4 M21 13v2a4 4 0 0 1-4 4H3",
            "desc1": "One internet-connected machine proxies all requests to the cloud — binaries, reporting, and terminal auto-tunnel through. Ideal for cross-subnet or firewalled hosts.",
            "val": "No need to expose every machine"
          },
          {
            "title": "PWA Offline Access",
            "color": "accent",
            "icon": "M12 18h.01M8 21h8a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2H8a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2z M9 7h.01",
            "desc1": "Installable to desktop (PWA), standalone window; App Shell offline cache shows last-known state even when disconnected.",
            "val": "Install on your phone, monitor anywhere"
          },
          {
            "title": "Python Plugin SDK",
            "color": "purple",
            "icon": "M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4",
            "desc1": "Built-in plugin SDK — collect custom metrics in a few lines (MySQL connections, Nginx requests, Redis memory, etc.).",
            "desc2": "Built-in examples: process monitor, service port probe, and more.",
            "val": "Monitor what you want, not just built-ins"
          },
          {
            "title": "Event Webhook Outbound",
            "color": "accent",
            "icon": "M22 2L11 13M22 2l-7 20-4-9-9-4 20-7z",
            "desc1": "Push alerts and incidents to your ticketing, IM, or custom platform — keep your existing ops workflow without being locked into one UI.",
            "val": "Alerts flow straight into your existing ticketing"
          },
          {
            "title": "Native Feishu / DingTalk / Email",
            "color": "warn",
            "icon": "M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z M22 6l-10 7L2 6",
            "desc1": "Five notification channels out of the box (Feishu / DingTalk / Email / SMS / Voice call); DingTalk webhook carries HMAC signing so delivery is both convenient and secure.",
            "val": "Alerts land in the collaboration tools your team already uses"
          },
          {
            "title": "Open Observability Data",
            "color": "accent",
            "icon": "M3 3v18h18M7 14l4-4 3 3 5-6",
            "desc1": "Time-series on VictoriaMetrics is compatible with the Prometheus remote-read/write protocol, so it coexists with your existing dashboards / alerting.",
            "val": "No rip-and-replace — just fill the gaps"
          }
        ]
      },
      {
        "tag": "09",
        "title": "Hardware Inspection & Storage Collection",
        "desc": "From server hardware to storage arrays — full-stack asset visibility",
        "pain": "Server hardware assets tracked by hand, storage array alerts only via vendor tools, completely disconnected from host monitoring?",
        "roles": ["Ops", "IT Asset Mgmt", "Storage Admin"],
        "items": [
          {
            "title": "Redfish Hardware Inspection",
            "color": "accent",
            "icon": "M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4",
            "desc1": "Standard Redfish/DMTF protocol remotely collects server hardware assets: CPU / memory / disks / RAID / NICs / fans / PSUs / temperatures — no agent needed.",
            "desc2": "Deep Huawei iBMC compatibility (ProcessorView / MemoryView one-shot collection), adapted for TaiShan / Kunpeng series.",
            "val": "Auto-discover hardware assets, no more manual spreadsheets"
          },
          {
            "title": "OceanStor Storage Collection",
            "color": "accent",
            "icon": "M2 20h20M5 20V10l7-6 7 6v10M9 20v-4h6v4",
            "desc1": "Collects storage pools / LUNs / controllers / disks / alerts via Huawei OceanStor RESTful API, unified into the monitoring dashboard.",
            "val": "Storage arrays and host assets on the same dashboard"
          },
          {
            "title": "NetFlow Traffic Analysis",
            "color": "accent",
            "icon": "M3 12h4l3-9 4 18 3-9h4",
            "desc1": "Built-in NetFlow v5/v9/IPFIX collector with 5-tuple (src/dst IP + port + protocol) traffic stats and TOP-N ranking, visual traffic heatmaps.",
            "desc2": "flow_records auto-partitioned by day, expired data auto-purged to prevent table bloat.",
            "val": "Spot abnormal traffic at a glance"
          },
          {
            "title": "Unified Asset Export",
            "color": "purple",
            "icon": "M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M16 13H8 M16 17H8 M10 9H8",
            "desc1": "Hardware inspection results exportable to Markdown / Excel / Word / PDF — zero third-party deps, perfect for asset audits and reporting.",
            "val": "One-click inspection reports, audit-ready"
          }
        ]
      },
      {
        "tag": "10",
        "title": "Mobile Ops",
        "desc": "The ops center in your pocket — full control, anywhere",
        "pain": "Can't see alerts, check hosts, or access terminals when you're away from your desk?",
        "roles": ["Ops", "On-Call", "Manager"],
        "items": [
          {
            "title": "Native Android App",
            "color": "ok",
            "icon": "M7 2h10a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2z M12 18h.01",
            "desc1": "Built with Kotlin + Jetpack Compose — not a WebView wrapper. Material 3 design, dark/light theme adaptive.",
            "desc2": "Host overview / alert push / remote terminal / hardware reports, smooth multi-host switching.",
            "val": "Native smooth experience — your phone is an ops tool too"
          },
          {
            "title": "PWA Mobile Adaptation",
            "color": "ok",
            "icon": "M12 18h.01M8 21h8a2 2 0 0 0 2-2V5a2 2 0 0 0-2-2H8a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2z M9 7h.01",
            "desc1": "Web dashboard installable to phone home screen (PWA), standalone window; App Shell offline cache shows last-known state even when disconnected.",
            "val": "No app install needed — browser is the entry point"
          },
          {
            "title": "Self-Hosted & Private",
            "color": "accent",
            "icon": "M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z",
            "desc1": "Custom server address, data always stays on your intranet — never passes through any third-party service. Login credentials share the same RBAC account system as the web dashboard.",
            "val": "Data never leaves your network, compliance worry-free"
          }
        ]
      }
    ],
    "cta.title": "All These Features in One Binary",
    "cta.desc": "No need for Prometheus + Grafana + Alertmanager + Bastion + Ticketing — AIOps does it all",
    "cta.btn2": "View Solutions"
  }
},

/* ---------- 产品对比页 ---------- */
"comparison": {
  "zh-CN": {
    "page.title": "产品对比 — AIOps",
    "page.desc": "与 Zabbix / Prometheus+Grafana / 商业 APM 全面对比：一体化 SRE 平台、私有化数据自主、用业界标准 PG+VM 存储开箱即扩的差异化优势。",
    "page.oglocale": "zh_CN",
    "head.tag": "产品对比", "head.title": "为什么选择 AIOps？", "head.desc": "一个平台覆盖 监控→告警→日志→终端→剧本→SRE→AI；数据留在你自己内网，用业界标准 PostgreSQL + VictoriaMetrics 承载，可从几台平滑扩到万级",
    "adv.tag": "核心优势", "adv.title": "中小企业选择 AIOps 的三个理由",
    "cta.title": "别再为监控工具的部署和维护买单了", "cta.desc": "把省下来的时间和预算，用在真正创造业务价值的事情上",
    "cta.btn1": "免费部署 →", "cta.btn2": "查看解决方案",
    "table": {
      "headers": ["能力维度","AIOps","Zabbix","Prometheus + Grafana","商业 APM"],
      "rows": [
        [["部署方式",""],["单二进制 + 一条命令","yes"],["Server + DB + Agent 多组件","no"],["Prometheus + Grafana + AlertManager","no"],["Agent + SaaS 账号","no"]],
        [["时序存储",""],["VictoriaMetrics（业界标准 · 高压缩 · 支撑万级）","yes"],["MySQL / PG（大规模写入吃力）",""],["本地 TSDB（单机保留有限）",""],["云端托管（数据上云）","no"]],
        [["部署时间",""],["3 分钟","yes"],["30-60 分钟","no"],["1-2 小时（含配置）","no"],["10-30 分钟","no"]],
        [["学习曲线",""],["低（开箱即用）","yes"],["中高（模板/触发器/Low-level discovery）","no"],["高（PromQL/YAML/Grafana 面板）","no"],["低-中",""]],
        [["远程终端",""],["内置（免开端口 + 会话录制回放 + 命令审计）","yes"],["需额外部署堡垒机","no"],["无","no"],["无","no"]],
        [["端口转发",""],["内置（TCP / UDP / HTTP + 端口范围批量 · 免开端口）","yes"],["无","no"],["无","no"],["无","no"]],
        [["自动化运维",""],["内置剧本编排","yes"],["无（需 Ansible 等配合）","no"],["无","no"],["无","no"]],
        [["SRE 闭环",""],["内置（事件 / 自动修复 / SLO / 工单）","yes"],["无","no"],["无","no"],["部分（需集成 PagerDuty 等）",""]],
        [["日志采集检索",""],["内置（增量采集 + 全文检索）","yes"],["无","no"],["需 Loki / ELK","no"],["部分",""]],
        [["AI 运维助手",""],["内置（巡检诊断 + 自主智能体 + RAG 知识库，可接 LLM）","yes"],["无","no"],["无","no"],["部分（付费）",""]],
        [["告警推送",""],["飞书/钉钉/邮件 + 阿里云/华为云/腾讯云多云短信与语音电话 + 桌面通知","yes"],["邮件/Webhook（需配置）",""],["AlertManager（需单独部署）","no"],["邮件/Slack/Webhook",""]],
        [["用户权限",""],["RBAC + MFA（内置）","yes"],["用户组（无 MFA）","no"],["无原生（需 Grafana 企业版）","no"],["有",""]],
        [["操作审计",""],["终端录制 + 回放 + 命令审计","yes"],["无终端审计","no"],["无","no"],["无终端审计","no"]],
        [["GPU 监控",""],["NVIDIA + AMD + Apple","yes"],["需自定义模板","no"],["需 DCGM Exporter","no"],["部分支持",""]],
        [["跨平台 Agent",""],["Linux/Win/macOS + ARM64","yes"],["Linux/Win/macOS",""],["Linux/Win（macOS 社区）","no"],["Linux/Win","no"]],
        [["PWA 移动端",""],["支持（可安装到手机桌面）+ 原生 Android App","yes"],["仅 Web","no"],["仅 Web","no"],["有 App（SaaS 绑定）",""]],
        [["原生 Android App",""],["Kotlin + Jetpack Compose（主机总览/告警/终端/报表）","yes"],["无","no"],["无","no"],["有（SaaS 绑定）",""]],
        [["硬件巡检（Redfish）",""],["内置（标准 Redfish + 华为 iBMC 兼容）","yes"],["需 IPMI 插件","no"],["无","no"],["部分（付费）",""]],
        [["NetFlow 流量分析",""],["内置（v5/v9/IPFIX + 五元组 TOP-N）","yes"],["无","no"],["需额外 Exporter","no"],["无","no"]],
        [["OceanStor 存储采集",""],["内置（RESTful API 采集存储池/LUN/控制器/告警）","yes"],["无","no"],["无","no"],["无","no"]],
        [["多服务端推送",""],["单 Agent 多服务端广播","yes"],["不支持","no"],["需 Remote Write","no"],["不支持","no"]],
        [["网关中继模式",""],["内置（跨网段穿透）","yes"],["需 Proxy/Agent 主动","no"],["需 Pushgateway","no"],["不支持","no"]],
        [["机器指纹鉴权",""],["machine-id + MAC 绑定","yes"],["PSK/Token","no"],["mTLS","no"],["Agent Key","no"]],
        [["gzip 压缩",""],["内置（8-10 倍压缩）","yes"],["需 Nginx 配置","no"],["需 Nginx 配置","no"],["有",""]],
        [["关系 / 审计存储",""],["PostgreSQL（配置/事件/工单/审计全持久化）","yes"],["MySQL / PostgreSQL",""],["无（仅指标）","no"],["云端托管",""]],
        [["数据自主 / 私有化",""],["全私有 · 数据不出内网","yes"],["私有",""],["私有",""],["数据上云","no"]],
        [["价格",""],["免费开源（AGPL-3.0）","yes"],["免费开源（GPL）",""],["免费开源（Apache）",""],["按主机数收费","no"]],
        [["适合规模",""],["几台 → 万级主机（VM 承载）","yes"],["50-5000+ 台",""],["100-10000+ 台",""],["任意（按量付费）",""]],
        [["告警降噪与分级",""],["严重/警告两级 + 去重冷却，告警量降约 80%","yes"],["无原生降噪",""],["需 AlertManager + 自定义",""],["有（付费）",""]],
        [["数据存储策略",""],["永久存储，不自动过期或删除","yes"],["需分区表/归档",""],["本地 TSDB 有限",""],["云端策略",""]],
        [["企业支持与服务",""],["开源社区 + 邮件支持，可选企业部署咨询/培训","yes"],["社区为主",""],["社区为主",""],["付费支持",""]]
      ]
    },
    "advantages": [
      {"title":"一体化闭环，替代 5+ 工具栈","color":"ok","icon":"M13 2L3 14h9l-1 8 10-12h-9l1-8z","desc":["一个二进制 = 指标 + 告警 + 日志 + 终端 + 剧本 + SRE 中枢 + AI 运维助手，不用再拼 Prometheus + Grafana + Alertmanager + ELK + 堡垒机 + 工单系统。","采集、告警、排障、修复、复盘在同一平台闭环，工具链与数据不再割裂。"],"value":"从「拼 5+ 工具」变成「一个平台全搞定」"},
      {"title":"企业级存储，一键即起","color":"accent","icon":"M5 13l4 4L19 7","desc":["一条 docker compose 同时拉起 服务端 + PostgreSQL + VictoriaMetrics，3 分钟上线——用业界标准存储承载，却省掉手工搭 DB / TSDB 的麻烦。","数据全部留在你自己的内网，随规模从几台平滑扩展到万级主机；配置密钥 AES-256-GCM 静态加密，不上云、不锁定。"],"value":"业界标准存储的底气 + 一键部署的省心"},
      {"title":"免费且开源","color":"purple","icon":"M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6","desc":["AGPL-3.0 开源协议，无商业限制。代码托管在 GitHub，透明可信。无主机数限制、无功能阉割、无“企业版”套路。","Python 插件 SDK 自由扩展，几行代码接入自定义指标。社区贡献持续迭代。"],"value":"零授权费、零主机数限制、零功能锁定"}
    ]
  },
  "zh-TW": {
    "page.title": "產品對比 — AIOps",
    "page.desc": "與 Zabbix / Prometheus+Grafana / 商業 APM 全面對比：一體化 SRE 平台、私有化資料自主、用業界標準 PG+VM 存儲開箱即擴的差異化優勢。",
    "page.oglocale": "zh_TW",
    "head.tag": "產品對比", "head.title": "為什麼選擇 AIOps？", "head.desc": "一個平台覆蓋 監控→告警→日誌→終端→劇本→SRE→AI；資料留在你自己內網，用業界標準 PostgreSQL + VictoriaMetrics 承載，可從幾台平滑擴到萬級",
    "adv.tag": "核心優勢", "adv.title": "中小企業選擇 AIOps 的三個理由",
    "cta.title": "別再為監控工具的部署和維護買單了", "cta.desc": "把省下來的時間和預算，用在真正創造業務價值的事情上",
    "cta.btn1": "免費部署 →", "cta.btn2": "查看解決方案",
    "table": {
      "headers": ["能力維度","AIOps","Zabbix","Prometheus + Grafana","商業 APM"],
      "rows": [
        [["部署方式",""],["單二進制 + 一條命令","yes"],["Server + DB + Agent 多組件","no"],["Prometheus + Grafana + AlertManager","no"],["Agent + SaaS 帳號","no"]],
        [["時序存儲",""],["VictoriaMetrics（業界標準 · 高壓縮 · 支撐萬級）","yes"],["MySQL / PG（大規模寫入吃力）",""],["本地 TSDB（單機保留有限）",""],["雲端託管（資料上雲）","no"]],
        [["部署時間",""],["3 分鐘","yes"],["30-60 分鐘","no"],["1-2 小時（含配置）","no"],["10-30 分鐘","no"]],
        [["學習曲線",""],["低（開箱即用）","yes"],["中高（模板/觸發器/Low-level discovery）","no"],["高（PromQL/YAML/Grafana 面板）","no"],["低-中",""]],
        [["遠程終端",""],["內建（免開端口 + 會話錄製回放 + 命令審計）","yes"],["需額外部署堡壘機","no"],["無","no"],["無","no"]],
        [["端口轉發",""],["內建（TCP / UDP / HTTP + 端口範圍批量 · 免開端口）","yes"],["無","no"],["無","no"],["無","no"]],
        [["自動化運維",""],["內建劇本編排","yes"],["無（需 Ansible 等配合）","no"],["無","no"],["無","no"]],
        [["SRE 閉環",""],["內建（事件 / 自動修復 / SLO / 工單）","yes"],["無","no"],["無","no"],["部分（需集成 PagerDuty 等）",""]],
        [["日誌採集檢索",""],["內建（增量採集 + 全文檢索）","yes"],["無","no"],["需 Loki / ELK","no"],["部分",""]],
        [["AI 運維助手",""],["內建（巡檢診斷 + 自主智能體 + RAG 知識庫，可接 LLM）","yes"],["無","no"],["無","no"],["部分（付費）",""]],
        [["告警推送",""],["飛書/釘釘/郵件 + 阿里雲/華為雲/騰訊雲多雲簡訊與語音電話 + 桌面通知","yes"],["郵件/Webhook（需配置）",""],["AlertManager（需單獨部署）","no"],["郵件/Slack/Webhook",""]],
        [["用戶權限",""],["RBAC + MFA（內建）","yes"],["用戶組（無 MFA）","no"],["無原生（需 Grafana 企業版）","no"],["有",""]],
        [["操作審計",""],["終端錄製 + 回放 + 命令審計","yes"],["無終端審計","no"],["無","no"],["無終端審計","no"]],
        [["GPU 監控",""],["NVIDIA + AMD + Apple","yes"],["需自定義模板","no"],["需 DCGM Exporter","no"],["部分支持",""]],
        [["跨平台 Agent",""],["Linux/Win/macOS + ARM64","yes"],["Linux/Win/macOS",""],["Linux/Win（macOS 社群）","no"],["Linux/Win","no"]],
        [["PWA 移動端",""],["支援（可安裝到手機桌面）+ 原生 Android App","yes"],["僅 Web","no"],["僅 Web","no"],["有 App（SaaS 綁定）",""]],
        [["原生 Android App",""],["Kotlin + Jetpack Compose（主機總覽/告警/終端/報表）","yes"],["無","no"],["無","no"],["有（SaaS 綁定）",""]],
        [["硬體巡檢（Redfish）",""],["內建（標準 Redfish + 華為 iBMC 相容）","yes"],["需 IPMI 插件","no"],["無","no"],["部分（付費）",""]],
        [["NetFlow 流量分析",""],["內建（v5/v9/IPFIX + 五元組 TOP-N）","yes"],["無","no"],["需額外 Exporter","no"],["無","no"]],
        [["OceanStor 儲存採集",""],["內建（RESTful API 採集儲存池/LUN/控制器/告警）","yes"],["無","no"],["無","no"],["無","no"]],
        [["多服務端推送",""],["單 Agent 多服務端廣播","yes"],["不支援","no"],["需 Remote Write","no"],["不支援","no"]],
        [["網關中繼模式",""],["內建（跨網段穿透）","yes"],["需 Proxy/Agent 主動","no"],["需 Pushgateway","no"],["不支援","no"]],
        [["機器指紋鑑權",""],["machine-id + MAC 綁定","yes"],["PSK/Token","no"],["mTLS","no"],["Agent Key","no"]],
        [["gzip 壓縮",""],["內建（8-10 倍壓縮）","yes"],["需 Nginx 配置","no"],["需 Nginx 配置","no"],["有",""]],
        [["關係 / 審計存儲",""],["PostgreSQL（配置/事件/工單/審計全持久化）","yes"],["MySQL / PostgreSQL",""],["無（僅指標）","no"],["雲端託管",""]],
        [["資料自主 / 私有化",""],["全私有 · 資料不出內網","yes"],["私有",""],["私有",""],["資料上雲","no"]],
        [["價格",""],["免費開源（AGPL-3.0）","yes"],["免費開源（GPL）",""],["免費開源（Apache）",""],["按主機數收費","no"]],
        [["適合規模",""],["幾台 → 萬級主機（VM 承載）","yes"],["50-5000+ 台",""],["100-10000+ 台",""],["任意（按量付費）",""]],
        [["告警降噪與分級",""],["嚴重/警告兩級 + 去重冷卻，告警量降約 80%","yes"],["無原生降噪",""],["需 AlertManager + 自定義",""],["有（付費）",""]],
        [["資料儲存策略",""],["永久儲存，不自動過期或刪除","yes"],["需分區表/歸檔",""],["本地 TSDB 有限",""],["雲端策略",""]],
        [["企業支援與服務",""],["開源社群 + 郵件支援，可選企業部署諮詢/培訓","yes"],["社群為主",""],["社群為主",""],["付費支援",""]]
      ]
    },
    "advantages": [
      {"title":"一體化閉環，替代 5+ 工具棧","color":"ok","icon":"M13 2L3 14h9l-1 8 10-12h-9l1-8z","desc":["一個二進制 = 指標 + 告警 + 日誌 + 終端 + 劇本 + SRE 中樞 + AI 運維助手，不用再拼 Prometheus + Grafana + Alertmanager + ELK + 堡壘機 + 工單系統。","採集、告警、排障、修復、複盤在同一平台閉環，工具鏈與資料不再割裂。"],"value":"從「拼 5+ 工具」變成「一個平台全搞定」"},
      {"title":"企業級存儲，一鍵即起","color":"accent","icon":"M5 13l4 4L19 7","desc":["一條 docker compose 同時拉起 服務端 + PostgreSQL + VictoriaMetrics，3 分鐘上線——用業界標準存儲承載，卻省掉手工搭 DB / TSDB 的麻煩。","資料全部留在你自己的內網，隨規模從幾台平滑擴展到萬級主機；配置密鑰 AES-256-GCM 靜態加密，不上雲、不鎖定。"],"value":"業界標準存儲的底氣 + 一鍵部署的省心"},
      {"title":"免費且開源","color":"purple","icon":"M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6","desc":["AGPL-3.0 開源協議，無商業限制。代碼託管在 GitHub，透明可信。無主機數限制、無功能閹割、無「企業版」套路。","Python 插件 SDK 自由擴展，幾行代碼接入自定義指標。社區貢獻持續迭代。"],"value":"零授權費、零主機數限制、零功能鎖定"}
    ]
  },
  "en": {
    "page.title": "Comparison — AIOps",
    "page.desc": "A full comparison with Zabbix / Prometheus+Grafana / commercial APM: an all-in-one SRE platform, self-hosted data ownership, and enterprise PG+VM storage that scales out of the box.",
    "page.oglocale": "en_US",
    "head.tag": "Comparison", "head.title": "Why Choose AIOps?", "head.desc": "One platform covering monitoring → alerts → logs → terminal → playbooks → SRE → AI; your data stays on your own network, backed by industry-standard PostgreSQL + VictoriaMetrics, scaling from a few to 10k+ hosts",
    "adv.tag": "Core Advantages", "adv.title": "Three Reasons SMBs Choose AIOps",
    "cta.title": "Stop Paying for Monitoring Tooling and Maintenance", "cta.desc": "Spend the saved time and budget on what actually creates business value",
    "cta.btn1": "Deploy Free →", "cta.btn2": "View Solutions",
    "table": {
      "headers": ["Capability","AIOps","Zabbix","Prometheus + Grafana","Commercial APM"],
      "rows": [
        [["Deployment",""],["Single binary + one command","yes"],["Server + DB + Agent components","no"],["Prometheus + Grafana + AlertManager","no"],["Agent + SaaS account","no"]],
        [["Time-series storage",""],["VictoriaMetrics (industry standard · high compression · scales to 10k+)","yes"],["MySQL / PG (write bottleneck at scale)",""],["Local TSDB (limited single-node retention)",""],["Cloud-hosted (data leaves your network)","no"]],
        [["Deploy time",""],["3 minutes","yes"],["30-60 minutes","no"],["1-2 hours (config incl.)","no"],["10-30 minutes","no"]],
        [["Learning curve",""],["Low (works out of the box)","yes"],["Medium-High (templates/triggers/LLD)","no"],["High (PromQL/YAML/Grafana)","no"],["Low-Medium",""]],
        [["Remote terminal",""],["Built-in (port-free + session recording & replay + command audit)","yes"],["Needs separate bastion host","no"],["None","no"],["None","no"]],
        [["Port forwarding",""],["Built-in (TCP / UDP / HTTP + port-range batch · port-free)","yes"],["None","no"],["None","no"],["None","no"]],
        [["Automation",""],["Built-in playbook orchestration","yes"],["None (needs Ansible etc.)","no"],["None","no"],["None","no"]],
        [["SRE closed loop",""],["Built-in (incidents / auto-remediation / SLO / tickets)","yes"],["None","no"],["None","no"],["Partial (needs PagerDuty etc.)",""]],
        [["Log collection & search",""],["Built-in (incremental tailing + full-text search)","yes"],["None","no"],["Needs Loki / ELK","no"],["Partial",""]],
        [["AI Ops Assistant",""],["Built-in (inspection & diagnosis + autonomous agent + RAG knowledge base, LLM-ready)","yes"],["None","no"],["None","no"],["Partial (paid)",""]],
        [["Alert delivery",""],["Feishu/DingTalk/Email + multi-cloud SMS & voice (Aliyun/Huawei/Tencent) + desktop notifications","yes"],["Email/Webhook (needs config)",""],["AlertManager (separate deploy)","no"],["Email/Slack/Webhook",""]],
        [["User permissions",""],["RBAC + MFA (built-in)","yes"],["User groups (no MFA)","no"],["Not native (needs Grafana Enterprise)","no"],["Yes",""]],
        [["Operation audit",""],["Terminal recording + replay + command audit","yes"],["No terminal audit","no"],["None","no"],["No terminal audit","no"]],
        [["GPU monitoring",""],["NVIDIA + AMD + Apple","yes"],["Custom template required","no"],["Needs DCGM Exporter","no"],["Partial",""]],
        [["Cross-platform agent",""],["Linux/Win/macOS + ARM64","yes"],["Linux/Win/macOS",""],["Linux/Win (macOS community)","no"],["Linux/Win","no"]],
        [["PWA mobile",""],["Yes (installable) + native Android App","yes"],["Web only","no"],["Web only","no"],["Has app (SaaS-tied)",""]],
        [["Native Android App",""],["Kotlin + Jetpack Compose (hosts/alerts/terminal/reports)","yes"],["None","no"],["None","no"],["Has app (SaaS-tied)",""]],
        [["Hardware inspection (Redfish)",""],["Built-in (standard Redfish + Huawei iBMC compatible)","yes"],["Needs IPMI plugin","no"],["None","no"],["Partial (paid)",""]],
        [["NetFlow traffic analysis",""],["Built-in (v5/v9/IPFIX + 5-tuple TOP-N)","yes"],["None","no"],["Needs extra Exporter","no"],["None","no"]],
        [["OceanStor storage collection",""],["Built-in (RESTful API for pools/LUNs/controllers/alerts)","yes"],["None","no"],["None","no"],["None","no"]],
        [["Multi-server push",""],["Single agent broadcasts to multiple servers","yes"],["Not supported","no"],["Needs Remote Write","no"],["Not supported","no"]],
        [["Gateway relay",""],["Built-in (cross-subnet tunnel)","yes"],["Needs Proxy/active Agent","no"],["Needs Pushgateway","no"],["Not supported","no"]],
        [["Machine fingerprint auth",""],["machine-id + MAC binding","yes"],["PSK/Token","no"],["mTLS","no"],["Agent Key","no"]],
        [["gzip compression",""],["Built-in (8-10x compression)","yes"],["Needs Nginx config","no"],["Needs Nginx config","no"],["Yes",""]],
        [["Relational / audit storage",""],["PostgreSQL (config/incidents/tickets/audit persisted)","yes"],["MySQL / PostgreSQL",""],["None (metrics only)","no"],["Cloud-hosted",""]],
        [["Data ownership / self-hosted",""],["Fully private · data never leaves your network","yes"],["Private",""],["Private",""],["Data goes to cloud","no"]],
        [["Pricing",""],["Free open source (AGPL-3.0)","yes"],["Free open source (GPL)",""],["Free open source (Apache)",""],["Per-host pricing","no"]],
        [["Best for",""],["A few → 10k+ hosts (VM-backed)","yes"],["50-5000+ hosts",""],["100-10000+ hosts",""],["Any (pay as you go)",""]],
        [["Alert de-noising & tiering",""],["Critical/warning tiers + dedup cooldown cut volume ~80%","yes"],["No native de-noising",""],["Needs AlertManager + custom",""],["Yes (paid)",""]],
        [["Data storage policy",""],["Permanent storage — no auto-expiry or deletion","yes"],["Needs partitioning/archiving",""],["Local TSDB limited",""],["Cloud policy",""]],
        [["Enterprise support & services",""],["Open-source community + email support, optional enterprise consulting/training","yes"],["Community-led",""],["Community-led",""],["Paid support",""]]
      ]
    },
    "advantages": [
      {"title":"All-in-One, Replaces 5+ Tools","color":"ok","icon":"M13 2L3 14h9l-1 8 10-12h-9l1-8z","desc":["One binary = metrics + alerts + logs + terminal + playbooks + SRE hub + AI Ops Assistant. No more stitching Prometheus + Grafana + Alertmanager + ELK + bastion + ticketing.","Collect, alert, troubleshoot, remediate and review in one closed loop — tools and data no longer fragmented."],"value":"From stitching 5+ tools to one platform for all"},
      {"title":"Enterprise Storage, One Command","color":"accent","icon":"M5 13l4 4L19 7","desc":["One docker compose brings up server + PostgreSQL + VictoriaMetrics in 3 minutes — industry-standard storage without the pain of hand-building a DB / TSDB.","All data stays on your own network and scales smoothly from a few hosts to 10k+; config secrets sealed with AES-256-GCM, no cloud, no lock-in."],"value":"Enterprise-grade storage with one-command simplicity"},
      {"title":"Free and Open Source","color":"purple","icon":"M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6","desc":["AGPL-3.0 licensed, no commercial restrictions. Code on GitHub, transparent and trustworthy. No host limits, no feature cuts, no enterprise-edition gimmicks.","Free Python plugin SDK — add custom metrics in a few lines. Community-driven iteration."],"value":'Zero license fees, zero host limits, zero lock-in'}
    ]
  }
},

/* ---------- 解决方案页 ---------- */
"solutions": {
  "zh-CN": {
    "page.title": "解决方案 — AIOps",
    "page.desc": "不用再为单机、多机房、团队与合规分别拼一套 Zabbix + Prometheus + Grafana。AIOps 用同一个私有化自托管平台，平滑覆盖你最头疼的七类真实运维场景 —— 一条命令部署，数据永久自持。",
    "page.oglocale": "zh_CN",
    "head.tag": "解决方案", "head.title": "真实场景，真实价值", "head.desc": "从单机到多机房，从故障应急到成本治理，覆盖你真实的运维场景",
    "cta.title": "无论你的运维场景是什么", "cta.desc": "单机也好，多机房也罢，团队协作或合规审计 —— AIOps 都能 3 分钟搞定",
    "cta.btn1": "免费部署 →", "cta.btn2": "查看产品对比",
    "scenarios": [
      {"num":"场景 01","title":"单机监控快速部署","result":"3 分钟完成私有化部署 · 零商业 license 费用 · 不限主机数","desc":"一台服务器跑业务，一个人管运维。没有专业监控团队，也没有预算买商业 APM。",
       "points":["下载 docker-compose.yml，一条命令自动生成并写入密钥","docker compose 一键起服务端 + PostgreSQL + VictoriaMetrics","3 分钟内完成部署，浏览器打开即用","一条 curl 命令在目标主机安装 Agent（自动检测架构）","飞书/钉钉 Webhook 配置后即收告警"],
       "visual":'<span style="color:var(--muted)"># 1. 通过 GitHub 下载</span><br>bash &lt;(curl -fsSL https://raw.githubusercontent.com/sreyun/aiops-monitor/master/scripts/secure-compose.sh)<br><span style="color:var(--muted)"># 通过 Gitee 镜像下载（GitHub 访问受限时推荐）</span><br>bash &lt;(curl -fsSL https://gitee.com/bigdatasafe/aiops-monitor/raw/master/scripts/secure-compose.sh)<br><span style="color:var(--muted)"># 2. 启动（密钥已写入，无需手动修改）</span><br>docker compose up -d<br><span style="color:var(--muted)"># 3. 在目标主机安装 Agent（按需，自动检测架构）</span><br>curl -fsSL "http://localhost:8529/install.sh?token=XXX" | sudo sh<br><br><span style="color:var(--ok)">✓ 浏览器打开 http://localhost:8529</span><br><span style="color:var(--muted)">默认凭据 admin / admin（首次登录强制修改）</span>'},
      {"num":"场景 02","title":"多机房集中监控","result":"1000+ 台主机统一纳管 · 故障发现从 30 分钟缩到 30 秒","desc":"多个机房、几十上百台机器，分散管理看不到全局。某台机器挂了半天才发现。",
       "points":["所有主机统一纳管到单一面板，按分类分组展示","Agent 支持多服务端推送，跨机房容灾不丢数据","网关中继模式：跨网段/防火墙后主机也能纳管","离线告警：主机 30 秒无上报即触发严重告警","概览页 KPI 卡片：在线/离线/严重告警/警告一目了然"],
       "visual":'<span style="color:var(--accent2)">[概览]</span> 15 台主机 · 14 在线 · 1 离线<br><span style="color:var(--ok)">●</span> web-01    CPU 23%  MEM 45%<br><span style="color:var(--ok)">●</span> web-02    CPU 18%  MEM 52%<br><span style="color:var(--ok)">●</span> db-master  CPU 67%  MEM 81%<br><span style="color:var(--crit)">●</span> db-slave   CPU  0%  MEM  0%<br><span style="color:var(--warn)">⚠</span> 告警: db-slave 已失联 120s<br><span style="color:var(--accent2)">[终端]</span> 点击 db-slave → 远程排查'},
      {"num":"场景 03","title":"团队协作运维","result":"操作 100% 可追溯 · 事故定责从「扯皮」到「有据」","desc":"多人运维但没有堡垒机，谁登了哪台机器、做了什么操作，完全没有记录。出了问题互相甩锅。",
       "points":["三级 RBAC：管理员 / 操作员 / 观察员权限隔离","远程终端全程录制，支持倍速回放追溯","操作日志：谁在什么时候对哪台主机做了什么","实时旁观：多人同时查看同一终端会话","MFA 两步验证 + 暴力破解防护"],
       "visual":'<span style="color:var(--accent2)">[终端会话]</span><br>操作者: <span style="color:var(--ok)">zhangsan</span><br>主机: db-master (10.0.1.5)<br>时间: 14:23:08<br><span style="color:var(--muted)">─ 命令审计 ─</span><br>$ top<br>$ systemctl restart nginx<br>$ tail -f /var/log/error.log<br><span style="color:var(--ok)">✓ 会话已录制 · 可回放</span>'},
      {"num":"场景 04","title":"等保合规审计","result":"合规审计材料从数周压缩到数小时 · 操作/权限/告警全留痕","desc":"等保测评要求操作可追溯、权限可管控、告警有记录。传统方式靠手动整理日志，费时费力。",
       "points":["全量操作日志：操作/系统/插件三类，支持筛选和 CSV 导出","终端会话录制 + 回放，满足操作可追溯要求","MFA + RBAC，满足访问控制要求","告警推送记录可查（飞书/钉钉/邮件/短信/语音电话）","PG + VM 统一存储：审计与历史数据重启不丢"],
       "visual":'<span style="color:var(--accent2)">[操作日志]</span><br>14:23  操作  zhangsan  打开远程终端<br>14:25  操作  zhangsan  终端命令: systemctl restart<br>14:30  系统  告警引擎  CPU 恢复正常<br>14:31  系统  通知引擎  飞书推送: 告警恢复<br>15:00  操作  lisi      执行剧本: 批量更新补丁<br>15:05  操作  lisi      剧本完成: 12/12 成功<br><span style="color:var(--muted)">─ 可导出 CSV ─</span>'},
      {"num":"场景 05","title":"故障应急与夜间值班","desc":"凌晨一条告警把你叫醒，但你不在电脑前，也不清楚该从哪里查起。传统「能 ping 通就好」的监控，出问题全靠人肉排查，MTTR 动辄小时级。",
       "points":["严重 / 警告两级告警 + 智能去重冷却，避免告警轰炸与疲劳","手机浏览器直接打开，远程终端秒级接入故障主机","AI 运维助手：巡检诊断定位根因，自主智能体给出并可直接执行处置建议，RAG 知识库沉淀历史经验","内置通知历史：谁在何时收到哪条告警，全程可追溯","剧本一键执行常见止血操作（重启服务 / 清理磁盘 / 拉起进程）"],
       "visual":'<span style="color:var(--warn)">⚠ 02:14 严重告警  web-02 CPU 98%</span><br><span style="color:var(--muted)"># 手机打开面板，秒级接入</span><br>$ top → 发现 java 进程 CPU 打满<br><span style="color:var(--accent2)">[AI 运维助手]</span> 根因: 缓存击穿 → 连接池耗尽<br><span style="color:var(--muted)"># 一键执行止血剧本</span><br>$ 执行剧本: 重启 gateway + 预热缓存<br><span style="color:var(--ok)">✓ 02:17 恢复正常 · MTTR 3 分钟</span>',
       "result":"MTTR 从平均 2 小时缩短至 15 分钟 · 夜间无效告警减少 80%"},
      {"num":"场景 06","title":"网站与业务可用性监控","desc":"官网 / 小程序挂了几个小时，还是客户先发现的。你缺一个 7×24 主动探活：SSL 证书过期、接口 5xx、域名解析异常，全都后知后觉。",
       "points":["HTTP / ICMP 主动探测，从服务端视角持续检查可用性","SSL 证书到期提前预警（默认提前 30 天）","接口状态码 / 响应耗时 / 关键字断言，偏离预期即告警","业务 API 拨测（apimon）：对核心业务接口做状态码 / 响应内容断言，比单纯探活更贴近真实用户体验","探测数据接入同一面板，与主机指标联动分析","飞书 / 钉钉 / 邮件 / 短信 / 语音电话多通道推送，故障第一时间触达"],
       "visual":'<span style="color:var(--accent2)">[可用性探测]</span><br><span style="color:var(--ok)">●</span> https://api.example.com   200  48ms<br><span style="color:var(--ok)">●</span> https://shop.example.com  200  62ms<br><span style="color:var(--crit)">●</span> https://pay.example.com  503  超时<br><span style="color:var(--warn)">⚠</span> SSL: pay.example.com 剩余 6 天<br><span style="color:var(--muted)">─ 近 24h 可用率 ─</span><br>api 100% · shop 99.98% · pay 98.20%',
       "result":"故障发现从「客户投诉」提前到「秒级探测」· 线上可用性可达 99.9%"},
      {"num":"场景 07","title":"成本优化与资源治理","desc":"云账单月月涨，却说不清哪些机器在空跑、哪些实例该降配。资源利用率是个黑盒，预算全靠拍脑袋。",
       "points":["CPU / 内存 / 磁盘 / 流量长期趋势，识别闲置与过载","低水位实例自动标注，给出降配 / 合并建议","容量预测辅助扩容决策，避免盲目堆配置","多机房资源汇总对比，僵尸资产一眼定位","历史曲线支撑成本复盘与下月预算编制"],
       "visual":'<span style="color:var(--accent2)">[资源利用率 · 近 30 天]</span><br>web-01   CPU 12%  MEM 30%  <span style="color:var(--warn)">低水位</span><br>web-02   CPU 15%  MEM 28%  <span style="color:var(--warn)">低水位</span><br>db-01    CPU 71%  MEM 84%  <span style="color:var(--ok)">正常</span><br>cache-01 CPU  3%  MEM 12%  <span style="color:var(--crit)">僵尸</span><br><span style="color:var(--muted)">─ 优化建议 ─</span><br>合并 web-01/02 · 回收 cache-01<br><span style="color:var(--ok)">预计月省 ¥1,820</span>',
       "result":"识别并回收闲置资源，云成本平均下降 20–35%"}
    ]
  },
  "zh-TW": {
    "page.title": "解決方案 — AIOps",
    "page.desc": "不用再為單機、多機房、團隊與合規分別拼一套 Zabbix + Prometheus + Grafana。AIOps 用同一個私有化自託管平台，平滑覆蓋你最頭疼的七類真實運維場景 —— 一條命令部署，資料永久自持。",
    "page.oglocale": "zh_TW",
    "head.tag": "解決方案", "head.title": "真實場景，真實價值", "head.desc": "從單機到多機房，從故障應急到成本治理，覆蓋你真實的運維場景",
    "cta.title": "無論你的運維場景是什麼", "cta.desc": "單機也好，多機房也罷，團隊協作或合規審計 —— AIOps 都能 3 分鐘搞定",
    "cta.btn1": "免費部署 →", "cta.btn2": "查看產品對比",
    "scenarios": [
      {"num":"場景 01","title":"單機監控快速部署","result":"3 分鐘完成私有化部署 · 零商業 license 費用 · 不限主機數","desc":"一台伺服器跑業務，一個人管運維。沒有專業監控團隊，也沒有預算買商業 APM。",
       "points":["下載 docker-compose.yml，一條命令自動生成並寫入密鑰","docker compose 一鍵起服務端 + PostgreSQL + VictoriaMetrics","3 分鐘內完成部署，瀏覽器打開即用","一條 curl 命令在目標主機安裝 Agent（自動檢測架構）","飛書/釘釘 Webhook 配置後即收告警"],
       "visual":'<span style="color:var(--muted)"># 1. 透過 GitHub 下載</span><br>bash &lt;(curl -fsSL https://raw.githubusercontent.com/sreyun/aiops-monitor/master/scripts/secure-compose.sh)<br><span style="color:var(--muted)"># 透過 Gitee 鏡像下載（GitHub 存取受限時推薦）</span><br>bash &lt;(curl -fsSL https://gitee.com/bigdatasafe/aiops-monitor/raw/master/scripts/secure-compose.sh)<br><span style="color:var(--muted)"># 2. 啟動（密鑰已寫入，無需手動修改）</span><br>docker compose up -d<br><span style="color:var(--muted)"># 3. 在目標主機安裝 Agent（依需，自動檢測架構）</span><br>curl -fsSL "http://localhost:8529/install.sh?token=XXX" | sudo sh<br><br><span style="color:var(--ok)">✓ 瀏覽器開啟 http://localhost:8529</span><br><span style="color:var(--muted)">預設憑據 admin / admin（首次登入強制修改）</span>'},
      {"num":"場景 02","title":"多機房集中監控","result":"1000+ 台主機統一納管 · 故障發現從 30 分鐘縮到 30 秒","desc":"多個機房、幾十上百台機器，分散管理看不到全局。某台機器掛了半天才發現。",
       "points":["所有主機統一納管到單一面板，按分類分組展示","Agent 支援多服務端推送，跨機房容災不丟數據","網關中繼模式：跨網段/防火牆後主機也能納管","離線告警：主機 30 秒無上報即觸發嚴重告警","概覽頁 KPI 卡片：在線/離線/嚴重告警/警告一目了然"],
       "visual":'<span style="color:var(--accent2)">[概覽]</span> 15 台主機 · 14 在線 · 1 離線<br><span style="color:var(--ok)">●</span> web-01    CPU 23%  MEM 45%<br><span style="color:var(--ok)">●</span> web-02    CPU 18%  MEM 52%<br><span style="color:var(--ok)">●</span> db-master  CPU 67%  MEM 81%<br><span style="color:var(--crit)">●</span> db-slave   CPU  0%  MEM  0%<br><span style="color:var(--warn)">⚠</span> 告警: db-slave 已失聯 120s<br><span style="color:var(--accent2)">[終端]</span> 點擊 db-slave → 遠程排查'},
      {"num":"場景 03","title":"團隊協作運維","result":"操作 100% 可追溯 · 事故定責從「扯皮」到「有據」","desc":"多人運維但沒有堡壘機，誰登了哪台機器、做了什麼操作，完全沒有記錄。出了問題互相甩鍋。",
       "points":["三級 RBAC：管理員 / 操作員 / 觀察員權限隔離","遠程終端全程錄製，支援倍速回放追溯","操作日誌：誰在什麼時候對哪台主機做了什麼","即時旁觀：多人同時查看同一終端會話","MFA 兩步驗證 + 暴力破解防護"],
       "visual":'<span style="color:var(--accent2)">[終端會話]</span><br>操作者: <span style="color:var(--ok)">zhangsan</span><br>主機: db-master (10.0.1.5)<br>時間: 14:23:08<br><span style="color:var(--muted)">─ 命令審計 ─</span><br>$ top<br>$ systemctl restart nginx<br>$ tail -f /var/log/error.log<br><span style="color:var(--ok)">✓ 會話已錄製 · 可回放</span>'},
      {"num":"場景 04","title":"等保合規審計","result":"合規審計材料從數週壓縮到數小時 · 操作/權限/告警全留痕","desc":"等保測評要求操作可追溯、權限可管控、告警有記錄。傳統方式靠手動整理日誌，費時費力。",
       "points":["全量操作日誌：操作/系統/插件三類，支援篩選和 CSV 匯出","終端會話錄製 + 回放，滿足操作可追溯要求","MFA + RBAC，滿足存取控制要求","告警推送記錄可查（飛書/釘釘/郵件/簡訊/語音電話）","PG + VM 統一存儲：審計與歷史數據重啟不丟"],
       "visual":'<span style="color:var(--accent2)">[操作日誌]</span><br>14:23  操作  zhangsan  打開遠程終端<br>14:25  操作  zhangsan  終端命令: systemctl restart<br>14:30  系統  告警引擎  CPU 恢復正常<br>14:31  系統  通知引擎  飛書推送: 告警恢復<br>15:00  操作  lisi      執行劇本: 批量更新補丁<br>15:05  操作  lisi      劇本完成: 12/12 成功<br><span style="color:var(--muted)">─ 可匯出 CSV ─</span>'},
      {"num":"場景 05","title":"故障應急與夜間值班","desc":"凌晨一條告警把你叫醒，但你不在電腦前，也不清楚該從哪裡查起。傳統「能 ping 通就好」的監控，出問題全靠人肉排查，MTTR 動輒小時級。",
       "points":["嚴重 / 警告兩級告警 + 智慧去重冷卻，避免告警轟炸與疲勞","手機瀏覽器直接開啟，遠程終端秒級接入故障主機","AI 運維助手：巡檢診斷定位根因，自主智能體給出並可直接執行處置建議，RAG 知識庫沉澱歷史經驗","內建通知歷史：誰在何時收到哪條告警，全程可追溯","劇本一鍵執行常見止血操作（重啟服務 / 清理磁碟 / 拉起程序）"],
       "visual":'<span style="color:var(--warn)">⚠ 02:14 嚴重告警  web-02 CPU 98%</span><br><span style="color:var(--muted)"># 手機開啟面板，秒級接入</span><br>$ top → 發現 java 程序 CPU 打滿<br><span style="color:var(--accent2)">[AI 運維助手]</span> 根因: 快取擊穿 → 連線池耗盡<br><span style="color:var(--muted)"># 一鍵執行止血劇本</span><br>$ 執行劇本: 重啟 gateway + 預熱快取<br><span style="color:var(--ok)">✓ 02:17 恢復正常 · MTTR 3 分鐘</span>',
       "result":"MTTR 從平均 2 小時縮短至 15 分鐘 · 夜間無效告警減少 80%"},
      {"num":"場景 06","title":"網站與業務可用性監控","desc":"官網 / 小程序掛了幾個小時，還是客戶先發現的。你缺一個 7×24 主動探活：SSL 憑證過期、介面 5xx、網域解析異常，全都後知後覺。",
       "points":["HTTP / ICMP 主動探測，從服務端視角持續檢查可用性","SSL 憑證到期提前預警（預設提前 30 天）","介面狀態碼 / 回應耗時 / 關鍵字斷言，偏離預期即告警","業務 API 撥測（apimon）：對核心業務介面做狀態碼 / 回應內容斷言，比單純探活更貼近真實用戶體驗","探測數據接入同一面板，與主機指標聯動分析","飛書 / 釘釘 / 郵件 / 簡訊 / 語音電話多通道推送，故障第一時間觸達"],
       "visual":'<span style="color:var(--accent2)">[可用性探測]</span><br><span style="color:var(--ok)">●</span> https://api.example.com   200  48ms<br><span style="color:var(--ok)">●</span> https://shop.example.com  200  62ms<br><span style="color:var(--crit)">●</span> https://pay.example.com  503  逾時<br><span style="color:var(--warn)">⚠</span> SSL: pay.example.com 剩餘 6 天<br><span style="color:var(--muted)">─ 近 24h 可用率 ─</span><br>api 100% · shop 99.98% · pay 98.20%',
       "result":"故障發現從「客戶投訴」提前到「秒級探測」· 線上可用性可達 99.9%"},
      {"num":"場景 07","title":"成本優化與資源治理","desc":"雲帳單月月漲，卻說不清哪些機器在空跑、哪些實例該降配。資源利用率是個黑盒，預算全靠拍腦袋。",
       "points":["CPU / 記憶體 / 磁碟 / 流量長期趨勢，識別閒置與過載","低水位實例自動標註，給出降配 / 合併建議","容量預測輔助擴容決策，避免盲目堆配置","多機房資源彙總對比，殭屍資產一眼定位","歷史曲線支撐成本複盤與下月預算編製"],
       "visual":'<span style="color:var(--accent2)">[資源利用率 · 近 30 天]</span><br>web-01   CPU 12%  MEM 30%  <span style="color:var(--warn)">低水位</span><br>web-02   CPU 15%  MEM 28%  <span style="color:var(--warn)">低水位</span><br>db-01    CPU 71%  MEM 84%  <span style="color:var(--ok)">正常</span><br>cache-01 CPU  3%  MEM 12%  <span style="color:var(--crit)">殭屍</span><br><span style="color:var(--muted)">─ 優化建議 ─</span><br>合併 web-01/02 · 回收 cache-01<br><span style="color:var(--ok)">預計月省 ¥1,820</span>',
       "result":"識別並回收閒置資源，雲成本平均下降 20–35%"}
    ]
  },
  "en": {
    "page.title": "Solutions — AIOps",
    "page.desc": "Tired of stitching together Zabbix + Prometheus + Grafana for every single-host, multi-DC, team, or compliance need? AIOps covers your seven toughest real-world ops scenarios on one self-hosted platform — deploy with one command, your data stays yours forever.",
    "page.oglocale": "en_US",
    "head.tag": "Solutions", "head.title": "Real Scenarios, Real Value", "head.desc": "From single host to multi-DC, from incident response to cost governance — your real ops scenarios",
    "cta.title": "Whatever Your Ops Scenario", "cta.desc": "Single host or multi-DC, team collaboration or compliance audit — AIOps gets it done in 3 minutes",
    "cta.btn1": "Deploy Free →", "cta.btn2": "View Comparison",
    "scenarios": [
      {"num":"Scenario 01","title":"Quick Single-Host Deploy","result":"Private deploy in 3 min · zero commercial license fee · unlimited hosts","desc":"One server runs your business, one person handles ops. No dedicated monitoring team, no budget for commercial APM.",
       "points":["Download docker-compose.yml; one command auto-generates and writes the secrets","One docker compose brings up server + PostgreSQL + VictoriaMetrics","Fully deployed in 3 minutes, open in the browser","Install the agent on the target host with one curl command (auto-detects arch)","Receive alerts right after configuring Feishu/DingTalk webhooks"],
       "visual":'<span style="color:var(--muted)"># 1. Via GitHub</span><br>bash &lt;(curl -fsSL https://raw.githubusercontent.com/sreyun/aiops-monitor/master/scripts/secure-compose.sh)<br><span style="color:var(--muted)"># Via Gitee mirror (recommended if GitHub is slow)</span><br>bash &lt;(curl -fsSL https://gitee.com/bigdatasafe/aiops-monitor/raw/master/scripts/secure-compose.sh)<br><span style="color:var(--muted)"># 2. Start (secrets already written, no manual edit)</span><br>docker compose up -d<br><span style="color:var(--muted)"># 3. Install the agent on the target host (optional, auto-detects arch)</span><br>curl -fsSL "http://localhost:8529/install.sh?token=XXX" | sudo sh<br><br><span style="color:var(--ok)">✓ Open http://localhost:8529 in your browser</span><br><span style="color:var(--muted)">Default credentials admin / admin (forced password change on first login)</span>'},
      {"num":"Scenario 02","title":"Centralized Multi-DC Monitoring","result":"1000+ hosts under one pane · failure detected in 30s, not 30 min","desc":"Multiple data centers, dozens to hundreds of hosts — fragmented management hides the big picture. A host goes down and you find out half an hour later.",
       "points":["All hosts unified in one dashboard, grouped by category","Agents push to multiple servers — cross-site DR, no data loss","Gateway relay: hosts behind firewalls/subnets still onboarded","Offline alert: 30s of no reporting triggers a critical alert","Overview KPI cards: online/offline/critical/warning at a glance"],
       "visual":'[Overview] 15 hosts · 14 online · 1 offline<br><span style="color:var(--ok)">●</span> web-01    CPU 23%  MEM 45%<br><span style="color:var(--ok)">●</span> web-02    CPU 18%  MEM 52%<br><span style="color:var(--ok)">●</span> db-master  CPU 67%  MEM 81%<br><span style="color:var(--crit)">●</span> db-slave   CPU  0%  MEM  0%<br><span style="color:var(--warn)">⚠</span> Alert: db-slave unreachable for 120s<br><span style="color:var(--accent2)">[Terminal]</span> click db-slave → remote troubleshoot'},
      {"num":"Scenario 03","title":"Team Collaboration Ops","result":"100% of operations traceable · accountability by evidence, not blame","desc":"Multi-admin ops without a bastion host — who logged into which machine and did what is completely unrecorded. When something breaks, everyone points fingers.",
       "points":["3-tier RBAC: admin / operator / viewer isolation","Full remote-terminal recording with speed-replay","Operation logs: who did what, when, on which host","Live observation: multiple people view the same session","MFA two-step + brute-force protection"],
       "visual":'[Terminal Session]<br>Operator: <span style="color:var(--ok)">zhangsan</span><br>Host: db-master (10.0.1.5)<br>Time: 14:23:08<br><span style="color:var(--muted)">─ Command Audit ─</span><br>$ top<br>$ systemctl restart nginx<br>$ tail -f /var/log/error.log<br><span style="color:var(--ok)">✓ Session recorded · replayable</span>'},
      {"num":"Scenario 04","title":"Compliance Audit","result":"Compliance evidence prepped in hours, not weeks · ops/access/alerts logged","desc":"Compliance assessments require traceable operations, controllable permissions, and logged alerts. The traditional way — manually collating logs — is slow and painful.",
       "points":["Full operation logs: operation/system/plugin, filterable and CSV-exportable","Terminal session recording + replay meets traceability requirements","MFA + RBAC meet access-control requirements","Alert delivery logs auditable (Feishu/DingTalk/Email/SMS/Voice call)","Unified PG + VM storage: audit & history survive restarts"],
       "visual":'[Operation Log]<br>14:23  op    zhangsan  opened remote terminal<br>14:25  op    zhangsan  terminal cmd: systemctl restart<br>14:30  sys   alert engine  CPU back to normal<br>14:31  sys   notify engine  Feishu push: alert recovered<br>15:00  op    lisi      ran playbook: batch patch update<br>15:05  op    lisi      playbook done: 12/12 succeeded<br><span style="color:var(--muted)">─ Exportable to CSV ─</span>'},
      {"num":"Scenario 05","title":"Incident Response & On-Call","desc":"An alert wakes you at 2 AM, but you are away from your desk and unsure where to start. Traditional ping-based monitoring leaves troubleshooting entirely manual — MTTR stretches to hours.",
       "points":["Critical / warning tiers + smart dedup cooling, no alert storms or fatigue","Open from your phone, remote terminal connects to the failing host in seconds","AI Ops Assistant: inspection pinpoints the root cause; the autonomous agent proposes and can directly run fixes; a RAG knowledge base distills past experience","Built-in notification history: who got which alert and when, fully traceable","One-click playbooks for common stopgaps (restart service / free disk / relaunch process)"],
       "visual":'<span style="color:var(--warn)">⚠ 02:14 critical  web-02 CPU 98%</span><br><span style="color:var(--muted)"># open dashboard on phone, connect in seconds</span><br>$ top → java process pegged at 100% CPU<br><span style="color:var(--accent2)">[AI Ops]</span> root cause: cache stampede → pool exhausted<br><span style="color:var(--muted)"># run stopgap playbook</span><br>$ run playbook: restart gateway + warm cache<br><span style="color:var(--ok)">✓ 02:17 recovered · MTTR 3 min</span>',
       "result":"MTTR cut from ~2h to 15 min · 80% fewer false-night alerts"},
      {"num":"Scenario 06","title":"Website & Service Uptime","desc":"Your site or mini-program was down for hours before a customer told you. You lack 24/7 active probing — SSL expiry, 5xx errors, DNS failures all surface too late.",
       "points":["Active HTTP / ICMP probing, continuously checking availability from the server","SSL certificate expiry warning (default 30 days ahead)","Status code / latency / keyword assertions; alert on deviation","Business API probing (apimon): assert on status code and response body of core endpoints — closer to real user experience than plain reachability","Probe data lands in the same dashboard, correlated with host metrics","Feishu / DingTalk / Email / SMS / Voice call multi-channel push, failure reaches you first"],
       "visual":'<span style="color:var(--accent2)">[Uptime Probe]</span><br><span style="color:var(--ok)">●</span> https://api.example.com   200  48ms<br><span style="color:var(--ok)">●</span> https://shop.example.com  200  62ms<br><span style="color:var(--crit)">●</span> https://pay.example.com  503  timeout<br><span style="color:var(--warn)">⚠</span> SSL: pay.example.com 6 days left<br><span style="color:var(--muted)">─ 24h availability ─</span><br>api 100% · shop 99.98% · pay 98.20%',
       "result":"Catch failures in seconds, not via customer complaints · 99.9% uptime"},
      {"num":"Scenario 07","title":"Cost Optimization & Resource Governance","desc":"The cloud bill creeps up every month, yet no one can say which boxes idle and which instances are oversized. Utilization is a black box; budgeting is guesswork.",
       "points":["Long-term CPU / memory / disk / traffic trends spot idle and overloaded hosts","Low-water hosts auto-flagged with downsize / merge suggestions","Capacity forecasting aids scale-up decisions, no blind over-provisioning","Cross-DC rollup compares resources; zombie assets surface at a glance","History curves back cost review and next-month budgeting"],
       "visual":'<span style="color:var(--accent2)">[Resource Utilization · 30d]</span><br>web-01   CPU 12%  MEM 30%  <span style="color:var(--warn)">low</span><br>web-02   CPU 15%  MEM 28%  <span style="color:var(--warn)">low</span><br>db-01    CPU 71%  MEM 84%  <span style="color:var(--ok)">ok</span><br>cache-01 CPU  3%  MEM 12%  <span style="color:var(--crit)">zombie</span><br><span style="color:var(--muted)">─ suggestions ─</span><br>merge web-01/02 · reclaim cache-01<br><span style="color:var(--ok)">est. save ¥1,820 / mo</span>',
       "result":"Reclaim idle resources, cloud cost down 20–35% on average"}
    ]
  }
},

/* ---------- 常见问题页 ---------- */
"faq": {
  "zh-CN": {
    "page.title": "常见问题 — AIOps",
    "page.desc": "关于 AIOps 的部署、安全、性能、扩展与端口转发，我们整理了最常见的疑问。",
    "page.oglocale": "zh_CN",
    "head.tag": "常见问题",
    "head.title": "关于 AIOps，你可能想问",
    "head.desc": "部署、安全、性能、扩展 —— 我们整理了最常见的疑问",
    "items": [
      {"q":"AIOps 免费吗？有功能限制吗？","a":"完全免费，采用 AGPL-3.0 开源协议，无主机数限制、无功能阉割、无「企业版」套路。代码托管在 GitHub，透明可信。"},
      {"q":"需要额外安装数据库或中间件吗？","a":"需要 PostgreSQL（配置/事件/工单/审计）+ VictoriaMetrics（指标/趋势）——均由 docker compose 一键拉起，无需单独运维。服务端与 Agent 本身仍是零第三方依赖的单个 Go 二进制。"},
      {"q":"支持哪些操作系统和架构？","a":"Agent 原生支持 Linux、Windows、macOS，覆盖 AMD64 与 ARM64。服务端为单一 Go 二进制，可在 1 核 1G 小型云服务器上运行。"},
      {"q":"不开放端口，远程终端和端口转发怎么工作？","a":"Agent 主动反向连接服务端，终端、转发、上报都走这条隧道；主机无需开放任何入站端口，天然适配防火墙 / NAT。只有管理员访问 Web 面板时才需放通服务端端口（默认 8529）。"},
      {"q":"能监控多少台主机？性能如何？","a":"设计目标是 1–5000+ 台。5 秒级采集、gzip 8–10 倍压缩、指标永久存储，单服务端占用极低；横向可通过多服务端推送与网关中继扩展。"},
      {"q":"和 Zabbix / Prometheus 相比优势在哪？","a":"一个二进制内置监控、告警、日志、终端、剧本、SRE 中枢与 AI 巡检，替代 5+ 工具栈；PostgreSQL + VictoriaMetrics + compose 一键起全套，3 分钟上线，学习曲线远低于 PromQL/YAML 体系。"},
      {"q":"端口转发支持哪些协议？能一次转发一段端口吗？","a":"支持 TCP / UDP / HTTP 单端口转发，以及 TCP / UDP 端口范围批量转发（单批最多 100 个），整组启停删除；走 Agent 隧道，内网服务零公网暴露。"},
      {"q":"数据存在哪里？会上传到第三方吗？","a":"全部数据保存在你自己的 PostgreSQL 与 VictoriaMetrics 中，部署在自有服务器或内网，绝不上传第三方云。100% 私有化自托管，可随时备份与导出。"},
      {"q":"如何升级？会影响正在监控的主机吗？","a":"拉取新镜像并重启容器（或替换二进制），数据持久化在 PG + VM 中不随容器销毁。Agent 支持断连重连与已知指纹免 Token 重注册，服务端重启后监控不中断。"},
      {"q":"AI 巡检需要额外付费或外部大模型吗？","a":"AI 能力内置且免费。未配置 Provider 时用启发式规则兜底；可对接自托管模型（Ollama / vLLM）或公有云 API。RAG 基于你自己的 pgvector，知识不出内网。"},
      {"q":"告警太多会疲劳吗？如何降噪？","a":"严重 / 警告两级 + 去重冷却（默认 5 分钟相同事件不重复推送），结合噪音抑制；提供保守 / 标准 / 宽松三档阈值预设，按场景一键切换。"},
      {"q":"如何与现有 Prometheus / Grafana / Zabbix 共存？","a":"不必推倒重来。AIOps 补齐终端、剧本、审计与 AI 诊断；VictoriaMetrics 兼容 Prometheus 远程读写，可与既有看板并存，按需平滑迁移。"},
      {"q":"数据保留多久？能否导出？","a":"指标 / 日志 / 审计均永久存储，不自动过期；操作日志与审计支持 CSV 导出；存储全在内网，数据主权完全掌握。"},
      {"q":"有手机端吗？支持哪些系统？","a":"提供原生 Android App（Kotlin + Jetpack Compose）与 HarmonyOS NEXT App（ArkTS），对接同一后端。支持实时总览、告警推送、移动终端与 AI 助手；推送走自建通道，不依赖第三方。"},
      {"q":"如何实现高可用？一个 Agent 能上报多个服务端吗？","a":"可以。Agent 支持 servers[] 多服务端：一次采集并发上报，配合重试与熔断容灾。服务端可置于反向代理后横向扩展；PG 与 VM 各自有高可用方案。"},
      {"q":"安全性与等保合规如何？","a":"会话 Cookie + RBAC、TOTP MFA、终端二次密码、机器指纹、安装令牌、AES-256-GCM 静态加密与可选 TLS；全量操作写入审计日志，可作等保审计证据链。"},
      {"q":"是否提供企业支持 / 等保咨询？","a":"产品本身完全免费开源、无功能阉割。同时提供私有化实施、定制开发、方案设计与培训等企业级支持，邮件联系即可。"}
    ],
    "cta.title": "还有疑问？直接动手试试",
    "cta.desc": "3 分钟部署，所有功能开箱即用，不满意随时卸载，不留任何外部依赖。",
    "cta.btn1": "免费部署 →", "cta.btn2": "查看功能详情"
  },
  "zh-TW": {
    "page.title": "常見問題 — AIOps",
    "page.desc": "關於 AIOps 的部署、安全、效能、擴展與端口轉發，我們整理了最常見的疑問。",
    "page.oglocale": "zh_TW",
    "head.tag": "常見問題",
    "head.title": "關於 AIOps，你可能想問",
    "head.desc": "部署、安全、效能、擴展 —— 我們整理了最常見的疑問",
    "items": [
      {"q":"AIOps 免費嗎？有功能限制嗎？","a":"完全免費，採用 AGPL-3.0 開源協議，無主機數限制、無功能閹割、無「企業版」套路。代碼託管在 GitHub，透明可信。"},
      {"q":"需要額外安裝資料庫或中間件嗎？","a":"需要 PostgreSQL（承載配置/事件/工單/審計等關係資料）+ VictoriaMetrics（承載指標/趨勢等時序資料）——但兩者都由 docker compose 隨服務端一鍵拉起，無需手動搭建或單獨運維。服務端與 Agent 本身仍是零第三方依賴的單個 Go 二進制。"},
      {"q":"支援哪些作業系統和架構？","a":"Agent 原生支援 Linux、Windows、macOS，覆蓋 AMD64 與 ARM64。服務端為單一 Go 二進制，可在 1 核 1G 的小型雲伺服器上運行。"},
      {"q":"不開放端口，遠程終端和端口轉發怎麼工作？","a":"Agent 主動反向連接服務端，所有通信（終端、轉發、上報）都走這條已建立的隧道，主機無需開放任何入站端口，天然適配防火牆 / NAT 環境。"},
      {"q":"能監控多少台主機？效能如何？","a":"設計目標是 1–5000+ 台主機。採集 5 秒級、gzip 8–10 倍壓縮、指標永久儲存，單台服務端資源占用極低，橫向可透過多服務端推送擴展。"},
      {"q":"和 Zabbix / Prometheus 相比優勢在哪？","a":"一個二進制內建監控、告警、日誌、終端、劇本、SRE 中樞與 AI 巡檢，替代 5+ 工具棧；用業界標準 PostgreSQL + VictoriaMetrics 存儲、compose 一鍵起全套，3 分鐘上線，學習曲線遠低於 PromQL/YAML 體系。"},
      {"q":"端口轉發支援哪些協定？能一次轉發一段端口嗎？","a":"支援 TCP / UDP / HTTP 三種協定的單端口轉發：TCP 適配資料庫、SSH、Web 後台等；UDP 適配 DNS、遊戲、音視訊等基於資料報的服務；HTTP 走無狀態代理隧道，可直接在瀏覽器訪問內網頁面。此外 TCP / UDP 還支援端口範圍批量轉發——填寫起訖端口即可一次性映射整段連續端口（單批最多 100 個），同一批次歸為一組，可整組啟用 / 停用 / 刪除，無需逐條操作。"},
      {"q":"數據存在哪裡？會上傳到雲端嗎？","a":"全部數據落在你部署的服務端本地，自託管、不依賴任何雲服務，也不會外傳。適合對數據主權有要求的場景。"},
      {"q":"如何升級？升級會影響正在監控的主機嗎？","a":"升級只需拉取新版本映像並重啟容器（或替換二進制後重啟），資料持久化在 PostgreSQL + VictoriaMetrics 中、不隨容器銷毀。Agent 支援斷線自動重連與已知指紋免 Token 重註冊，服務端重啟後監控不中斷。"},
      {"q":"AI 巡檢與診斷需要額外配置大模型或 API Key 嗎？","a":"不需要。未配置 AI Provider 時由內建啟發式規則兜底研判，開箱即用；配置 LLM 後升級為智能體級分析。錯誤 / 告警日誌自動納入分析上下文，判斷更貼近現場。"},
      {"q":"告警太多會疲勞嗎？如何降噪？","a":"支援嚴重 / 警告兩級 + 事件去重冷卻（預設 5 分鐘內相同事件不重複推送），結合噪音抑制，告警量可降低約 80%；還提供保守 / 標準 / 寬鬆三檔閾值預設，依場景一鍵切換。"},
      {"q":"如何與現有監控棧（Prometheus / Grafana / Zabbix）共存？","a":"不必推倒重來。AIOps 補齊終端、劇本、審計與 AI 診斷等短板；VictoriaMetrics 相容 Prometheus 遠程讀寫，可與既有看板並存；多服務端推送讓你在新舊監控間平滑過渡、按需遷移。"},
      {"q":"資料保留多久？能否匯出？","a":"所有監控資料（指標 / 日誌 / 審計）均永久儲存，不自動過期或刪除；操作日誌與審計支援 CSV 匯出；PostgreSQL 與 VictoriaMetrics 均在你自己的內網，可隨時備份與遷移，資料主權完全掌握。"},
      {"q":"大規模（5000+ 台）部署如何調優？","a":"5 秒級採集 + gzip 8–10 倍壓縮 + 指標永久儲存，單服務端資源佔用極低；橫向透過多服務端推送擴展；網關中繼減少出口長連線數量，弱網環境也能穩定納管。"},
      {"q":"是否提供企業版 / 商業支援 / 等保諮詢？","a":"產品本身完全免費開源、無功能閹割。我們同時提供企業級支援：私有化實施、客製開發、方案設計與培訓等，可透過郵件聯繫；等保相關的審計、權限、MFA 能力均已內建。"}
    ,
        {"q":"我的監控數據存在哪裡？會被上傳到第三方嗎？","a":"全部資料保存在你自己的 PostgreSQL 與 VictoriaMetrics 中，部署在你自有的伺服器或內網，絕不上傳任何第三方雲。平台 100% 私有化自托管，資料永久自持、可隨時匯出。"},
        {"q":"Agent 需要開放入站埠嗎？防火牆怎麼配？","a":"不需要。Agent 主動「反向連線」服務端上報與拉取指令，服務端無須對 Agent 開放任何入站埠；只有管理員存取 Web 面板 / 終端時才需要放通服務端埠（預設 8529）。這天然適配公網服務端 + 內網 Agent 的拓撲。"},
        {"q":"AI 診斷 / 巡檢需要額外付費或外部大模型嗎？","a":"AI 能力內建且免費，採可插拔 LLM 架構：既可對接你自託管開源模型（如 Ollama / vLLM），也可設定公有雲 API。檢索增強（RAG）基於你自己的 pgvector 記憶庫，案例與知識都不出內網。"},
        {"q":"有手機端嗎？支援哪些系統？","a":"提供原生 Android App（Kotlin + Jetpack Compose，20+ 螢幕）與 HarmonyOS NEXT App（ArkTS），均對接同一套後端。安裝包獨立分發（行動端原始碼不在本倉庫）。支援即時總覽、告警推送、行動終端與 AI 助手；推送走自建 /ws/push，不依賴第三方推送服務。"},
        {"q":"如何實現高可用？一個 Agent 能上報多個服務端嗎？","a":"可以。Agent 支援多服務端（servers[]）設定：一次採集並發上報到多個服務端，配合重試與熔斷實現容災。服務端本身無狀態（除記憶體態與會話），可置於反向代理 / 負載平衡之後橫向擴展；儲存層 PostgreSQL 與 VictoriaMetrics 各自提供高可用方案。"},
        {"q":"安全性如何？滿足等保 / 合規審計要求嗎？","a":"提供會話 Cookie + RBAC（admin/operator/viewer）、TOTP MFA、終端二次密碼、Agent 機器指紋、安裝令牌（7 天寬限）、設定密鑰 AES-256-GCM 靜態加密與可選 TLS；全量操作寫入審計日誌。相關能力契合等保審計與合規要求，可作為審計證據鏈的一部分。"}
      ],
    "cta.title": "還有疑問？直接動手試試",
    "cta.desc": "3 分鐘部署，所有功能開箱即用，不滿意隨時卸載，不留任何外部依賴。",
    "cta.btn1": "免費部署 →", "cta.btn2": "查看功能詳情"
  },
  "en": {
    "page.title": "FAQ — AIOps",
    "page.desc": "Common questions about AIOps: deployment, security, performance, extensibility, and port forwarding.",
    "page.oglocale": "en_US",
    "head.tag": "FAQ",
    "head.title": "Common Questions About AIOps",
    "head.desc": "Deployment, security, performance, extensibility — the questions we hear most",
    "items": [
      {"q":"Is AIOps free? Any limits?","a":"Completely free under the AGPL-3.0 license — no host limits, no feature cuts, no enterprise-edition gimmicks. Code is on GitHub, transparent and trustworthy."},
      {"q":"Do I need a database or middleware?","a":"Yes — PostgreSQL (relational data: config/incidents/tickets/audit) and VictoriaMetrics (time-series: metrics/trends). Both are brought up automatically by docker compose alongside the server, so there's no manual setup or separate ops. The server and agent themselves remain single Go binaries with zero third-party dependencies."},
      {"q":"Which OS and architectures are supported?","a":"Agents natively support Linux, Windows, macOS on both AMD64 and ARM64. The server is one Go binary that runs on a 1-core 1GB cloud instance."},
      {"q":"No open ports — how do terminal and forwarding work?","a":"The agent connects to the server reversely; all traffic (terminal, forwarding, reporting) flows over that established tunnel, so hosts need no inbound ports — ideal behind firewalls/NAT."},
      {"q":"How many hosts can it monitor? Performance?","a":"Designed for 1–5000+ hosts. 5-second collection, 8–10x gzip compression, and permanent metrics storage keep server overhead minimal; scale out via multi-server push."},
      {"q":"How is it better than Zabbix / Prometheus?","a":"One binary bundles monitoring, alerting, logs, terminal, playbooks, an SRE hub and AI inspection — replacing a 5+ tool stack. Backed by industry-standard PostgreSQL + VictoriaMetrics and brought up by one compose command; 3-minute deploy, far lower learning curve than PromQL/YAML."},
      {"q":"Which protocols does port forwarding support? Can it forward a whole port range at once?","a":"Single-port forwarding works over TCP, UDP, and HTTP: TCP for databases, SSH, and web backends; UDP for datagram services like DNS, gaming, and media; HTTP over a stateless proxy tunnel so you can open internal pages right in your browser. TCP and UDP also support port-range batch forwarding — enter a start and end port to map an entire contiguous range in one shot (up to 100 ports per batch). Each batch is grouped so you can enable / disable / delete the whole set together instead of one rule at a time."},
      {"q":"Where is my data? Does it leave my network?","a":"All data stays on your self-hosted server — no cloud dependency, nothing sent externally. Ideal when data sovereignty matters."},
      {"q":"How do I upgrade? Does it affect monitored hosts?","a":"Upgrade by pulling the new image and restarting the container (or swapping the binary and restarting). Data lives in PostgreSQL + VictoriaMetrics and survives container recreation. Agents auto-reconnect and re-register by known fingerprint, so monitoring continues across server restarts."},
      {"q":"Does AI inspection need an LLM or API key?","a":"No. Without an AI Provider configured, built-in heuristic rules provide a fallback — works out of the box. With an LLM configured it upgrades to agentic analysis. Error/alert logs are fed into the analysis context automatically for more grounded judgments."},
      {"q":"Will I get alert fatigue? How is noise reduced?","a":"Alerts are tiered (critical/warning) with dedupe + cooldown (same event not re-pushed within 5 min by default), cutting volume ~80%. Three threshold presets — conservative/standard/relaxed — let you tune sensitivity per environment in one click."},
      {"q":"How does it coexist with my existing stack (Prometheus / Grafana / Zabbix)?","a":"No rip-and-replace. AIOps fills the gaps — terminal, playbooks, audit, and AI diagnosis; VictoriaMetrics is Prometheus remote-read/write compatible so it coexists with your dashboards; multi-server push lets you migrate smoothly between old and new monitoring."},
      {"q":"How long is data retained? Can it be exported?","a":"All monitoring data (metrics / logs / audit) is stored permanently — never auto-expired or deleted; operation and audit logs export to CSV; both PostgreSQL and VictoriaMetrics live on your own network, so you can back up and migrate anytime with full data ownership."},
      {"q":"How do I tune large-scale (5000+ host) deployments?","a":"5-second collection, 8–10x gzip, and permanent metrics storage keep single-server overhead minimal; scale out via multi-server push; gateway relay cuts outbound long connections so even weak-network hosts onboard stably."},
      {"q":"Do you offer an enterprise edition / commercial support / compliance consulting?","a":"The product itself is free and open source with no feature cuts. We also provide enterprise support — private deployment, custom development, solution design, and training — reachable by email; audit, RBAC, and MFA for compliance are already built in."}
    ,
        {"q":"Where is my monitoring data stored? Does it leave my network?","a":"All data stays in your own PostgreSQL and VictoriaMetrics, deployed on your servers or intranet — nothing is uploaded to any third party. The platform is 100% self-hosted; your data is permanently yours and fully exportable."},
        {"q":"Does the Agent need an open inbound port? How should I set up the firewall?","a":"No. The Agent proactively connects outbound to the server to report and pull commands, so the server needs no inbound port open for Agents. Only admin access to the web console or terminal requires opening the server port (default 8529). This fits a public server plus private Agent topology naturally."},
        {"q":"Do AI diagnosis or inspection features require paid or external LLMs?","a":"AI is built in and free, with a pluggable LLM architecture: it can use your self-hosted open-source models (e.g. Ollama or vLLM) or a public-cloud API. Retrieval-augmented generation (RAG) runs on your own pgvector memory store, so cases and knowledge never leave your network."},
        {"q":"Is there a mobile app? Which platforms are supported?","a":"Yes — a native Android app (Kotlin + Jetpack Compose, 20+ screens) and a HarmonyOS NEXT app (ArkTS), both reusing the same backend. Packages are distributed externally (mobile source is not in this repo). They support live overview, alert push, mobile terminal, and the AI assistant; push uses a self-built /ws/push long connection with no third-party push service."},
        {"q":"How do I achieve high availability? Can one Agent report to multiple servers?","a":"Yes. The Agent supports multiple servers (servers[]): one collection is reported concurrently to all servers, with retry and circuit-breaking for disaster recovery. The server itself is stateless aside from in-memory state and sessions, and can sit behind a reverse proxy or load balancer for horizontal scale; PostgreSQL and VictoriaMetrics each provide their own HA options."},
        {"q":"How secure is it? Does it meet compliance and audit requirements?","a":"It provides session cookies plus RBAC (admin/operator/viewer), TOTP MFA, terminal secondary password, Agent machine fingerprint, install token (7-day grace), AES-256-GCM static encryption of config secrets, and optional TLS; all operations are written to an audit log. These capabilities align with compliance and audit requirements and can serve as part of an audit evidence chain."}
      ],
    "cta.title": "Still Curious? Just Try It",
    "cta.desc": "Deploy in 3 minutes, every feature works out of the box, uninstall anytime with zero leftover dependencies.",
    "cta.btn1": "Deploy Free →", "cta.btn2": "View Features"
  }
},

/* ---------- 联系我们 ---------- */
"contact": {
  "zh-CN": {
    "page.title": "联系我们 — AIOps",
    "page.desc": "有合作、部署、定制或反馈需求？通过邮件或 GitHub 与 AIOps 团队取得联系。",
    "page.oglocale": "zh_CN",
    "head.tag": "联系我们",
    "head.title": "我们随时乐意为你提供帮助",
    "head.desc": "无论是部署咨询、功能建议、商务合作还是问题反馈，都欢迎与我们联系",
    "c.email.title": "电子邮件",
    "c.email.desc": "商务合作、部署咨询、定制开发与一般问题，邮件是最可靠的联系方式，我们通常在 1–2 个工作日内回复。",
    "c.email.btn": "发送邮件",
    "c.issue.title": "问题反馈",
    "c.issue.desc": "发现 Bug 或有明确的功能需求？在 GitHub Issues 提交，可追踪、可讨论，团队会公开跟进处理。",
    "c.issue.btn": "提交 Issue",
    "c.repo.title": "开源社区",
    "c.repo.desc": "关注项目动态、查阅文档、参与讨论或贡献代码，欢迎 Star 与 Fork，一起把产品做得更好。",
    "c.repo.btn": "访问仓库",
    "resp.title": "我们承诺",
    "resp.i1.t": "1–2 个工作日", "resp.i1.d": "邮件通常在两个工作日内回复",
    "resp.i2.t": "公开透明", "resp.i2.d": "Issues 与讨论全程公开可追踪",
    "resp.i3.t": "认真对待", "resp.i3.d": "每一条建议与反馈都会被评估",
    "subscribe.title": "订阅产品动态",
    "subscribe.desc": "留下邮箱，第一时间获取新版本、部署技巧与最佳实践。",
    "subscribe.placeholder": "你的邮箱地址 *",
    "subscribe.btn": "订阅",
    "subscribe.note": "我们尊重你的隐私，绝不发送垃圾邮件。",
    "subscribe.ok": "已记录，感谢关注！我们会尽快与你联系。",
    "subscribe.invalid": "请输入有效的邮箱地址。",
    "subscribe.phonePlaceholder": "手机号（选填）",
    "subscribe.storageErr": "存储失败，浏览器存储已满。",
    "subscribe.dup": "你已订阅，我们会及时推送最新动态！",
    "ent.tag": "企业服务",
    "ent.title": "为企业级部署而生",
    "ent.desc": "从私有化落地到规模化运维，我们提供贴合企业场景的咨询、实施与培训。无论你处在选型、POC 还是规模扩张阶段，都能找到合适的支持路径。",
    "ent.deploy.tag": "部署形态",
    "ent.deploy.title": "三种部署形态，适配不同规模",
    "ent.deploy.items": [
      {"t":"私有化标准版","d":"单节点 docker compose 一键起，适合中小团队与单一机房，3 分钟上线、零外部依赖。","icon":"M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"},
      {"t":"高可用集群版","d":"多服务端推送 + 跨机房容灾，关键业务不间断监控；适合多机房与大规模主机纳管。","icon":"M12 2L2 7v10l10 5 10-5V7L12 2z"},
      {"t":"混合云纳管","d":"网关中继打通跨网段 / 防火墙后主机，总部与分支、云上与本地统一监控于一屏。","icon":"M17 1l4 4-4 4 M3 11V9a4 4 0 0 1 4-4h14 M7 23l-4-4 4-4 M21 13v2a4 4 0 0 1-4 4H3"}
    ],
    "ent.support.tag": "支持与 SLA",
    "ent.support.title": "分层支持，按需选择",
    "ent.support.items": [
      {"t":"社区版（免费）","d":"GitHub 开源社区 + 完整文档，问题在 Issue 公开跟进，AGPL-3.0 协议无功能阉割。","icon":"M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"},
      {"t":"标准支持","d":"邮件优先响应（1–2 工作日）+ 部署咨询与最佳实践，适合已上线、需要稳定保障的团队。","icon":"M5 13l4 4L19 7"},
      {"t":"企业支持","d":"专属方案架构师、私有化实施、定制开发与培训，支持等保合规与大规模落地。","icon":"M9 12l2 2 4-4 M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z"}
    ],
    "ent.process.tag": "合作流程",
    "ent.process.title": "四步启动企业级运维",
    "ent.process.items": [
      {"n":"01","t":"需求沟通","d":"了解你的规模、架构与痛点，明确监控与合规目标。"},
      {"n":"02","t":"方案设计","d":"给出私有化 / 集群 / 混合部署建议与集成方案。"},
      {"n":"03","t":"POC 验证","d":"在真实环境小范围试点，验证关键能力。"},
      {"n":"04","t":"落地与培训","d":"全量上线 + 团队培训，建立可持续运维流程。"}
    ],
    "ent.trust.tag": "为什么选择我们",
    "ent.trust.title": "企业关心的，我们早已考虑",
    "ent.trust.items": [
      {"t":"数据主权","d":"全私有部署，数据不出内网，密钥 AES-256-GCM 加密。","icon":"M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4"},
      {"t":"安全合规","d":"RBAC + MFA + 终端审计 + 操作日志，契合等保审计对可溯源的要求。","icon":"M9 12l2 2 4-4 M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z"},
      {"t":"开源透明","d":"AGPL-3.0 协议，代码公开可审，无锁定、无隐藏收费。","icon":"M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"},
      {"t":"平滑扩展","d":"从单机到万级主机，多服务端推送与网关中继弹性扩展。","icon":"M13 2L3 14h9l-1 8 10-12h-9l1-8z"}
    ],
    "cta.title": "准备好开始了吗？",
    "cta.desc": "3 分钟完成部署，所有功能开箱即用。有任何问题，随时给我们发邮件。",
    "cta.btn1": "免费部署 →", "cta.btn2": "查看功能详情",
    "contact.form.tag": "留下联系方式",
    "contact.form.title": "让我们主动联系您",
    "contact.form.desc": "填写邮箱和手机号，我们的团队会在 1-2 个工作日内与您取得联系",
    "contact.form.name": "姓名",
    "contact.form.namePh": "您的姓名",
    "contact.form.email": "邮箱",
    "contact.form.phone": "手机号",
    "contact.form.phonePh": "13800138000",
    "contact.form.message": "需求描述",
    "contact.form.msgPh": "简单描述您的需求或问题",
    "contact.form.submit": "提交信息",
    "contact.form.privacy": "我们尊重您的隐私，信息仅用于与您联系",
    "contact.invalidEmail": "请输入有效的邮箱地址",
    "contact.invalidPhone": "请输入有效的手机号码",
    "contact.tooLong": "输入内容超出长度限制",
    "contact.submitting": "提交中...",
    "contact.success": "提交成功，我们会尽快与您联系！",
    "contact.updated": "信息已更新，感谢您的关注！",
    "contact.storageErr": "存储空间不足，请清空旧数据后重试"
  },
  "zh-TW": {
    "page.title": "聯絡我們 — AIOps",
    "page.desc": "有合作、部署、客製或回饋需求？透過電子郵件或 GitHub 與 AIOps 團隊取得聯繫。",
    "page.oglocale": "zh_TW",
    "head.tag": "聯絡我們",
    "head.title": "我們隨時樂意為你提供協助",
    "head.desc": "無論是部署諮詢、功能建議、商務合作還是問題回饋，都歡迎與我們聯繫",
    "c.email.title": "電子郵件",
    "c.email.desc": "商務合作、部署諮詢、客製開發與一般問題，電子郵件是最可靠的聯繫方式，我們通常在 1–2 個工作日內回覆。",
    "c.email.btn": "發送郵件",
    "c.issue.title": "問題回饋",
    "c.issue.desc": "發現 Bug 或有明確的功能需求？在 GitHub Issues 提交，可追蹤、可討論，團隊會公開跟進處理。",
    "c.issue.btn": "提交 Issue",
    "c.repo.title": "開源社群",
    "c.repo.desc": "關注專案動態、查閱文檔、參與討論或貢獻代碼，歡迎 Star 與 Fork，一起把產品做得更好。",
    "c.repo.btn": "造訪倉庫",
    "resp.title": "我們的承諾",
    "resp.i1.t": "1–2 個工作日", "resp.i1.d": "電子郵件通常在兩個工作日內回覆",
    "resp.i2.t": "公開透明", "resp.i2.d": "Issues 與討論全程公開可追蹤",
    "resp.i3.t": "認真對待", "resp.i3.d": "每一條建議與回饋都會被評估",
    "subscribe.title": "訂閱產品動態",
    "subscribe.desc": "留下電子郵件，第一時間獲取新版本、部署技巧與最佳實踐。",
    "subscribe.placeholder": "你的電子郵件地址 *",
    "subscribe.btn": "訂閱",
    "subscribe.note": "我們尊重你的隱私，絕不發送垃圾郵件。",
    "subscribe.ok": "感謝訂閱！我們會透過郵件與你保持聯繫。",
    "subscribe.invalid": "請輸入有效的電子郵件地址。",
    "subscribe.phonePlaceholder": "手機號碼（選填）",
    "subscribe.storageErr": "儲存失敗，瀏覽器儲存已滿。",
    "subscribe.dup": "你已訂閱，我們會及時推送最新動態！",
    "ent.tag": "企業服務",
    "ent.title": "為企業級部署而生",
    "ent.desc": "從私有化落地到規模化運維，我們提供貼合企業場景的諮詢、實施與培訓。無論你處在選型、POC 還是規模擴張階段，都能找到合適的支持路徑。",
    "ent.deploy.tag": "部署形態",
    "ent.deploy.title": "三種部署形態，適配不同規模",
    "ent.deploy.items": [
      {"t":"私有化標準版","d":"單節點 docker compose 一鍵起，適合中小團隊與單一機房，3 分鐘上線、零外部依賴。","icon":"M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"},
      {"t":"高可用叢集版","d":"多服務端推送 + 跨機房容災，關鍵業務不間斷監控；適合多機房與大規模主機納管。","icon":"M12 2L2 7v10l10 5 10-5V7L12 2z"},
      {"t":"混合雲納管","d":"網關中繼打通跨網段 / 防火牆後主機，總部與分支、雲上與本地統一監控於一屏。","icon":"M17 1l4 4-4 4 M3 11V9a4 4 0 0 1 4-4h14 M7 23l-4-4 4-4 M21 13v2a4 4 0 0 1-4 4H3"}
    ],
    "ent.support.tag": "支援與 SLA",
    "ent.support.title": "分層支援，按需選擇",
    "ent.support.items": [
      {"t":"社群版（免費）","d":"GitHub 開源社群 + 完整文件，問題在 Issue 公開跟進，AGPL-3.0 協議無功能閹割。","icon":"M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"},
      {"t":"標準支援","d":"郵件優先響應（1–2 工作日）+ 部署諮詢與最佳實踐，適合已上線、需要穩定保障的團隊。","icon":"M5 13l4 4L19 7"},
      {"t":"企業支援","d":"專屬方案架構師、私有化實施、客製開發與培訓，支援等保合規與大規模落地。","icon":"M9 12l2 2 4-4 M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z"}
    ],
    "ent.process.tag": "合作流程",
    "ent.process.title": "四步啟動企業級運維",
    "ent.process.items": [
      {"n":"01","t":"需求溝通","d":"了解你的規模、架構與痛點，明確監控與合規目標。"},
      {"n":"02","t":"方案設計","d":"給出私有化 / 叢集 / 混合部署建議與整合方案。"},
      {"n":"03","t":"POC 驗證","d":"在真實環境小範圍試點，驗證關鍵能力。"},
      {"n":"04","t":"落地與培訓","d":"全量上線 + 團隊培訓，建立可持續運維流程。"}
    ],
    "ent.trust.tag": "為什麼選擇我們",
    "ent.trust.title": "企業關心的，我們早已考慮",
    "ent.trust.items": [
      {"t":"資料主權","d":"全私有部署，資料不出內網，密鑰 AES-256-GCM 加密。","icon":"M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4"},
      {"t":"安全合規","d":"RBAC + MFA + 終端審計 + 操作日誌，契合等保審計對可溯源的要求。","icon":"M9 12l2 2 4-4 M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z"},
      {"t":"開源透明","d":"AGPL-3.0 協議，代碼公開可審，無鎖定、無隱藏收費。","icon":"M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"},
      {"t":"平滑擴展","d":"從單機到萬級主機，多服務端推送與網關中繼彈性擴展。","icon":"M13 2L3 14h9l-1 8 10-12h-9l1-8z"}
    ],
    "cta.title": "準備好開始了嗎？",
    "cta.desc": "3 分鐘完成部署，所有功能開箱即用。有任何問題，隨時給我們發郵件。",
    "cta.btn1": "免費部署 →", "cta.btn2": "查看功能詳情",
    "contact.form.tag": "留下聯繫方式",
    "contact.form.title": "讓我們主動聯繫您",
    "contact.form.desc": "填寫郵箱和手機號，我們的團隊會在 1-2 個工作日內與您取得聯繫",
    "contact.form.name": "姓名",
    "contact.form.namePh": "您的姓名",
    "contact.form.email": "郵箱",
    "contact.form.phone": "手機號",
    "contact.form.phonePh": "13800138000",
    "contact.form.message": "需求描述",
    "contact.form.msgPh": "簡單描述您的需求或問題",
    "contact.form.submit": "提交資訊",
    "contact.form.privacy": "我們尊重您的隱私，資訊僅用於與您聯繫",
    "contact.invalidEmail": "請輸入有效的郵箱地址",
    "contact.invalidPhone": "請輸入有效的手機號碼",
    "contact.tooLong": "輸入內容超出長度限制",
    "contact.submitting": "提交中...",
    "contact.success": "提交成功，我們會盡快與您聯繫！",
    "contact.updated": "資訊已更新，感謝您的關注！",
    "contact.storageErr": "儲存空間不足，請清空舊資料後重試"
  },
  "en": {
    "page.title": "Contact — AIOps",
    "page.desc": "Partnership, deployment, customization, or feedback? Reach the AIOps team by email or GitHub.",
    "page.oglocale": "en_US",
    "head.tag": "Contact",
    "head.title": "We're Always Happy to Help",
    "head.desc": "Deployment questions, feature ideas, business partnerships, or feedback — we'd love to hear from you",
    "c.email.title": "Email",
    "c.email.desc": "For partnerships, deployment questions, custom development, and general inquiries, email is the most reliable way to reach us. We typically reply within 1–2 business days.",
    "c.email.btn": "Send Email",
    "c.issue.title": "Report an Issue",
    "c.issue.desc": "Found a bug or have a concrete feature request? File it on GitHub Issues — trackable, discussable, and publicly followed up by the team.",
    "c.issue.btn": "Open an Issue",
    "c.repo.title": "Open Source Community",
    "c.repo.desc": "Follow the project, read the docs, join discussions, or contribute code. Star and fork us — let's make it better together.",
    "c.repo.btn": "Visit Repo",
    "resp.title": "Our Commitment",
    "resp.i1.t": "1–2 Business Days", "resp.i1.d": "Emails are usually answered within two business days",
    "resp.i2.t": "Open & Transparent", "resp.i2.d": "Issues and discussions are public and trackable",
    "resp.i3.t": "Taken Seriously", "resp.i3.d": "Every suggestion and report gets evaluated",
    "subscribe.title": "Subscribe to product updates",
    "subscribe.desc": "Drop your email to get new releases, deployment tips and best practices first.",
    "subscribe.placeholder": "you@example.com *",
    "subscribe.btn": "Subscribe",
    "subscribe.note": "We respect your privacy and never send spam.",
    "subscribe.ok": "Saved — thanks! We'll reach out soon.",
    "subscribe.invalid": "Please enter a valid email address.",
    "subscribe.phonePlaceholder": "Phone (optional)",
    "subscribe.storageErr": "Storage failed, browser storage is full.",
    "subscribe.dup": "You're already subscribed, we'll keep you posted!",
    "ent.tag": "Enterprise Services",
    "ent.title": "Built for Enterprise-Grade Deployment",
    "ent.desc": "From private rollout to scaled operations, we offer consulting, implementation, and training tailored to enterprise scenarios. Whether you're evaluating, running a POC, or scaling out, there's a support path for you.",
    "ent.deploy.tag": "Deployment Forms",
    "ent.deploy.title": "Three deployment forms for any scale",
    "ent.deploy.items": [
      {"t":"Private Standard","d":"Single-node docker compose up in one command — ideal for small teams and a single DC; 3-minute launch with zero external dependencies.","icon":"M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"},
      {"t":"HA Cluster","d":"Multi-server push + cross-site DR keep monitoring uninterrupted for critical business — fit for multi-DC and large host fleets.","icon":"M12 2L2 7v10l10 5 10-5V7L12 2z"},
      {"t":"Hybrid Cloud","d":"Gateway relay onboards firewalled / cross-subnet hosts; HQ, branches, cloud and on-prem all monitored on one screen.","icon":"M17 1l4 4-4 4 M3 11V9a4 4 0 0 1 4-4h14 M7 23l-4-4 4-4 M21 13v2a4 4 0 0 1-4 4H3"}
    ],
    "ent.support.tag": "Support & SLA",
    "ent.support.title": "Tiered support, choose what you need",
    "ent.support.items": [
      {"t":"Community (Free)","d":"GitHub open-source community + full docs; issues are tracked publicly. AGPL-3.0 licensed, no feature cuts.","icon":"M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"},
      {"t":"Standard Support","d":"Priority email response (1–2 business days) + deployment consulting and best practices — for teams already in production needing stable assurance.","icon":"M5 13l4 4L19 7"},
      {"t":"Enterprise Support","d":"Dedicated solution architect, private implementation, custom development and training; compliance and large-scale rollout ready.","icon":"M9 12l2 2 4-4 M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z"}
    ],
    "ent.process.tag": "Engagement Process",
    "ent.process.title": "Four steps to enterprise-grade ops",
    "ent.process.items": [
      {"n":"01","t":"Discovery","d":"Understand your scale, architecture, and pain points; clarify monitoring and compliance goals."},
      {"n":"02","t":"Design","d":"Recommend private / cluster / hybrid deployment and integration plans."},
      {"n":"03","t":"POC","d":"Pilot in a real, small-scoped environment to validate key capabilities."},
      {"n":"04","t":"Rollout & Training","d":"Full rollout + team training to build a sustainable ops process."}
    ],
    "ent.trust.tag": "Why Choose Us",
    "ent.trust.title": "What enterprises care about, already handled",
    "ent.trust.items": [
      {"t":"Data Sovereignty","d":"Fully private deployment; data never leaves your network; secrets AES-256-GCM encrypted.","icon":"M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z M9 12l2 2 4-4"},
      {"t":"Security & Compliance","d":"RBAC + MFA + terminal audit + operation logs meet traceability requirements.","icon":"M9 12l2 2 4-4 M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z"},
      {"t":"Open & Transparent","d":"AGPL-3.0 licensed, code publicly auditable, no lock-in, no hidden fees.","icon":"M12 1v22M17 5H9.5a3.5 3.5 0 0 0 0 7h5a3.5 3.5 0 0 1 0 7H6"},
      {"t":"Smooth Scaling","d":"From a single host to 10k+, multi-server push and gateway relay scale elastically.","icon":"M13 2L3 14h9l-1 8 10-12h-9l1-8z"}
    ],
    "cta.title": "Ready to Get Started?",
    "cta.desc": "Deploy in 3 minutes with every feature out of the box. Got questions? Just drop us an email.",
    "cta.btn1": "Deploy Free →", "cta.btn2": "View Features",
    "contact.form.tag": "Leave Your Contact",
    "contact.form.title": "Let Us Reach Out to You",
    "contact.form.desc": "Fill in your email and phone number, our team will contact you within 1-2 business days",
    "contact.form.name": "Name",
    "contact.form.namePh": "Your name",
    "contact.form.email": "Email",
    "contact.form.phone": "Phone",
    "contact.form.phonePh": "13800138000",
    "contact.form.message": "Message",
    "contact.form.msgPh": "Briefly describe your needs or issues",
    "contact.form.submit": "Submit",
    "contact.form.privacy": "We respect your privacy. Information is used for contact only.",
    "contact.invalidEmail": "Please enter a valid email address",
    "contact.invalidPhone": "Please enter a valid phone number",
    "contact.tooLong": "Input exceeds maximum length",
    "contact.submitting": "Submitting...",
    "contact.success": "Submitted! We'll get back to you soon.",
    "contact.updated": "Info updated, thanks for your interest!",
    "contact.storageErr": "Storage full. Please clear old data and retry."
  }
}

}; /* end T */

/* 合并扩展字典（i18n-extra.js 注入的 window.__I18N_EXTRA） */
if (typeof window !== "undefined" && window.__I18N_EXTRA) {
  (function mergeExtra(dst, src) {
    Object.keys(src).forEach(function (k) {
      if (src[k] && typeof src[k] === "object" && !Array.isArray(src[k])) {
        dst[k] = dst[k] || {};
        mergeExtra(dst[k], src[k]);
      } else {
        dst[k] = src[k];
      }
    });
  })(T, window.__I18N_EXTRA);
}


/* ============================================================
   语言检测 / 切换 / 持久化
   ============================================================ */

/* 从 URL ?lang= 参数获取语言 */
function detectFromURL() {
  var params = new URLSearchParams(window.location.search);
  var lang = params.get("lang");
  if (lang && SUPPORTED.indexOf(lang) >= 0) return lang;
  return null;
}

/* 从 localStorage 获取语言 */
function detectFromStorage() {
  try {
    var lang = localStorage.getItem("aiops_lang");
    if (lang && SUPPORTED.indexOf(lang) >= 0) return lang;
  } catch(e) {}
  return null;
}

/* 从浏览器语言偏好检测 */
function detectFromBrowser() {
  var nav = navigator.language || navigator.userLanguage || "";
  nav = nav.toLowerCase();
  if (nav.indexOf("zh-tw") >= 0 || nav.indexOf("zh-hk") >= 0 || nav.indexOf("zh-mo") >= 0 || nav.indexOf("zh-hant") >= 0) return "zh-TW";
  if (nav.indexOf("zh") >= 0) return "zh-CN";
  if (nav.indexOf("ko") >= 0) return "ko";
  return "en";
}

/* 获取当前页面名 */
function getPageName() {
  var path = window.location.pathname.split("/").pop() || "index.html";
  return path.replace(".html", "").replace("-en", "");
}

/* 获取翻译 */
function t(key) {
  var page = getPageName();
  var common = T["_common"] || {};
  var pageT = T[page] || {};
  var dict = common[CURRENT_LANG] || {};
  var pageDict = pageT[CURRENT_LANG] || {};
  return pageDict[key] || dict[key] || (T["_common"] && T["_common"][DEFAULT_LANG] && T["_common"][DEFAULT_LANG][key]) || (T[page] && T[page][DEFAULT_LANG] && T[page][DEFAULT_LANG][key]) || key;
}

/* HTML 转义 */
function esc(s) {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

/* 渲染对比表格 + 优势卡片 */
function renderComparison(d) {
  var tbl = document.getElementById("cmpTable");
  if (tbl && d && d.table) {
    var h = d.table.headers, rows = d.table.rows;
    var thead = "<thead><tr>" + h.map(function(hh, i) {
      return "<th" + (i === 1 ? ' class="col-highlight"' : "") + ">" + esc(hh) + "</th>";
    }).join("") + "</tr></thead>";
    var tbody = "<tbody>" + rows.map(function(r) {
      return "<tr>" + r.map(function(cell, i) {
        var cls = cell[1] || "";
        if (i === 0) return '<td class="feat-name">' + esc(cell[0]) + "</td>";
        /* highlight AIOps column (index 1) */
        var classes = [];
        if (cls) classes.push(cls);
        if (i === 1) classes.push("col-highlight");
        var c = classes.length ? ' class="' + classes.join(" ") + '"' : "";
        return "<td" + c + ">" + esc(cell[0]) + "</td>";
      }).join("") + "</tr>";
    }).join("") + "</tbody>";
    tbl.innerHTML = thead + tbody;
  }
  var adv = document.getElementById("cmpAdvantages");
  if (adv && d && d.advantages) {
    var softMap = { ok: "var(--ok-soft)", accent: "var(--accent-soft)", purple: "var(--purple-soft)" };
    var borderMap = { ok: "var(--ok-border)", accent: "var(--accent-border)", purple: "var(--purple-border)" };
    adv.innerHTML = d.advantages.map(function(a) {
      var bg = softMap[a.color] || "var(--accent-soft)";
      var bd = borderMap[a.color] || "var(--accent-border)";
      var descs = a.desc.map(function(p) { return '<p class="desc">' + esc(p) + "</p>"; }).join("");
      return '<div class="feature-card">'
        + '<div class="glow"></div>'
        + '<div class="feature-icon" style="background:' + bg + ';border-color:' + bd + '">'
        + '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="' + a.icon + '"/></svg></div>'
        + "<h3>" + esc(a.title) + "</h3>"
        + descs
        + '<div class="value"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="20 6 9 17 4 12"/></svg>' + esc(a.value) + "</div>"
        + "</div>";
    }).join("");
  }
}

/* 渲染解决方案场景 */
function renderSolutions(d) {
  var list = document.getElementById("scnList");
  if (list && d && d.scenarios) {
    list.innerHTML = d.scenarios.map(function(s, i) {
      var reverse = (i % 2 === 1);
      var result = s.result ? '<div class="scenario-result"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="20 6 9 17 4 12"/></svg><span>' + esc(s.result) + "</span></div>" : "";
      var text = '<div><div class="scenario-num">' + esc(s.num) + "</div>"
        + "<h3>" + esc(s.title) + "</h3>"
        + "<p>" + esc(s.desc) + "</p>"
        + "<ul>" + s.points.map(function(p) { return "<li>" + esc(p) + "</li>"; }).join("") + "</ul>"
        + result + "</div>";
      var vis = '<div class="scenario-visual"><div class="mockup">' + s.visual + "</div></div>";
      return '<div class="scenario">' + (reverse ? vis + text : text + vis) + "</div>";
    }).join("");
  }
}

/* 渲染功能分组（features 页） */
function renderFeatures(d) {
  var wrap = document.getElementById("featGroups");
  if (!wrap || !d || !d.groups) return;
  var softMap = { ok: "var(--ok-soft)", accent: "var(--accent-soft)", warn: "var(--warn-soft)", purple: "var(--purple-soft)" };
  var borderMap = { ok: "var(--ok-border)", accent: "var(--accent-border)", warn: "var(--warn-border)", purple: "var(--purple-border)" };
  var BADGE_TAGS = { "06": "exclusive", "04": "hot" };
  var BADGE_TEXT = {
    "exclusive": { "zh-CN": "独家", "zh-TW": "獨家", "en": "Exclusive", "ja": "独占", "ko": "단독" },
    "hot": { "zh-CN": "热门", "zh-TW": "熱門", "en": "Popular", "ja": "人気", "ko": "인기" }
  };
  /* 客户痛点带 */
  var painsEl = document.getElementById("featPains");
  if (painsEl && d.pains) {
    painsEl.innerHTML = d.pains.map(function(p) {
      return '<div class="pain-card reveal">'
        + '<div class="pain-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="' + p.icon + '"/></svg></div>'
        + "<h3>" + esc(p.t) + "</h3><p>" + esc(p.d) + "</p></div>";
    }).join("");
  }
  wrap.innerHTML = d.groups.map(function(g) {
    var cards = g.items.map(function(it) {
      var bg = softMap[it.color] || "var(--accent-soft)";
      var bd = borderMap[it.color] || "var(--accent-border)";
      var descs = [it.desc1, it.desc2].filter(function(x) { return x; })
        .map(function(p) { return '<p class="desc">' + esc(p) + "</p>"; }).join("");
      return '<div class="feature-card reveal' + (it.hl ? " hl" : "") + '">'
        + '<div class="glow"></div>'
        + '<div class="feature-icon" style="background:' + bg + ';border-color:' + bd + '">'
        + '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="' + it.icon + '"/></svg></div>'
        + "<h3>" + esc(it.title) + "</h3>"
        + descs
        + '<div class="feat-val"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 3l1.5 4.5L18 9l-4.5 1.5L12 15l-1.5-4.5L6 9l4.5-1.5z"/></svg>' + esc(it.val) + "</div>"
        + "</div>";
    }).join("");
    var roles = (g.roles || []).map(function(r) { return '<span class="chip">' + esc(r) + "</span>"; }).join("");
    return '<div class="feat-group reveal" id="feat-' + esc(g.tag) + '">'
      + '<div class="feat-group-head">'
      + '<span class="feat-group-tag">' + esc(g.tag) + "</span>"
      + (BADGE_TAGS[g.tag] ? ' <span class="feat-badge">' + esc(BADGE_TEXT[BADGE_TAGS[g.tag]][CURRENT_LANG]) + "</span>" : "")
      + "<div><h3>" + esc(g.title) + "</h3><p>" + esc(g.desc) + "</p></div>"
      + "</div>"
      + (g.pain ? '<div class="feat-pain"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 9v4M12 17h.01M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/></svg><span>' + esc(g.pain) + "</span></div>" : "")
      + (roles ? '<div class="feat-roles">' + roles + "</div>" : "")
      + '<div class="feature-grid">' + cards + "</div>"
      + "</div>";
  }).join("");
  var sub = document.getElementById("featSubnav");
  if (sub && d.groups) {
    sub.innerHTML = d.groups.map(function(g) {
      return '<a class="feat-subnav-link" href="#feat-' + esc(g.tag) + '" title="' + esc(g.title) + '">'
        + '<span class="rail-num">' + esc(g.tag) + "</span>"
        + '<span class="rail-txt">' + esc(g.title) + "</span>"
        + "</a>";
    }).join("");
  }
}

/* 渲染常见问题手风琴（faq 页） */
function renderFaq(d) {
  var list = document.getElementById("faqList");
  if (!list || !d || !d.items) return;
  list.innerHTML = d.items.map(function(it) {
    return '<div class="faq-item reveal">'
      + '<button class="faq-q" type="button" aria-expanded="false">'
      + "<span>" + esc(it.q) + "</span>"
      + '<svg class="faq-chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="6 9 12 15 18 9"/></svg>'
      + "</button>"
      + '<div class="faq-a"><p>' + esc(it.a) + "</p></div>"
      + "</div>";
  }).join("");
  list.querySelectorAll(".faq-q").forEach(function(btn) {
    btn.addEventListener("click", function() {
      var item = btn.parentElement;
      var ans = item.querySelector(".faq-a");
      var open = item.classList.toggle("open");
      btn.setAttribute("aria-expanded", open ? "true" : "false");
      /* 按内容实际高度展开，兼容超长答案与切换语言后的重渲染 */
      ans.style.maxHeight = open ? (ans.scrollHeight + "px") : "0px";
    });
  });
}

/* 渲染首页动态区块（技术生态集成 + 端口转发亮点） */
function renderIndex(d) {
  var grid = document.getElementById("integrations");
  if (grid && d && d.integrations) {
    grid.innerHTML = d.integrations.map(function(it) {
      return '<div class="integration-item reveal">'
        + '<div class="integration-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="' + it.icon + '"/></svg></div>'
        + '<span>' + esc(it.name) + "</span>"
        + "</div>";
    }).join("");
  }
  var pts = document.getElementById("fwdPoints");
  if (pts && d && d["fwd.points"]) {
    pts.innerHTML = d["fwd.points"].map(function(p) {
      return '<li><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="20 6 9 17 4 12"/></svg><span>' + esc(p) + "</span></li>";
    }).join("");
  }

  var prev = document.getElementById("faqPreview");
  if (prev) {
    var faqData = (T.faq && T.faq[CURRENT_LANG] && T.faq[CURRENT_LANG].items) || [];
    prev.innerHTML = faqData.slice(0, 3).map(function(it) {
      return '<a class="faq-preview-card reveal" href="faq.html">'
        + '<span class="faq-pc-q">' + esc(it.q) + "</span>"
        + '<span class="faq-pc-a">' + esc(it.a) + "</span>"
        + '<span class="faq-pc-go" aria-hidden="true">→</span>'
        + "</a>";
    }).join("");
  }
}

/* 渲染联系页企业级区块（部署形态 / 支持分层 / 合作流程 / 信任要素）
   注：ent.* 在本页以平铺点号 key 存储（如 "ent.deploy.items"），故此处直接按平铺 key 读取 */
function renderContact(d) {
  if (!d) return;
  var softMap = { ok: "var(--ok-soft)", accent: "var(--accent-soft)", warn: "var(--warn-soft)", purple: "var(--purple-soft)" };
  var borderMap = { ok: "var(--ok-border)", accent: "var(--accent-border)", warn: "var(--warn-border)", purple: "var(--purple-border)" };
  function card(it, cls) {
    var bg = softMap[it.color] || "var(--accent-soft)";
    var bd = borderMap[it.color] || "var(--accent-border)";
    return '<div class="' + cls + ' reveal"><div class="glow"></div>'
      + '<div class="feature-icon" style="background:' + bg + ';border-color:' + bd + '">'
      + '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="' + it.icon + '"/></svg></div>'
      + "<h3>" + esc(it.t) + "</h3>"
      + '<p class="desc">' + esc(it.d) + "</p></div>";
  }
  var deploy = d["ent.deploy.items"];
  if (deploy) {
    var el = document.getElementById("entDeploy");
    if (el) el.innerHTML = deploy.map(function(it) { return card(it, "feature-card"); }).join("");
  }
  var support = d["ent.support.items"];
  if (support) {
    var el2 = document.getElementById("entSupport");
    if (el2) el2.innerHTML = support.map(function(it) { return card(it, "feature-card"); }).join("");
  }
  var trust = d["ent.trust.items"];
  if (trust) {
    var el3 = document.getElementById("entTrust");
    if (el3) el3.innerHTML = trust.map(function(it) { return card(it, "feature-card"); }).join("");
  }
  var proc = d["ent.process.items"];
  if (proc) {
    var el4 = document.getElementById("entProcess");
    if (el4) el4.innerHTML = proc.map(function(it) {
      return '<div class="ent-step reveal"><div class="ent-step-n">' + esc(it.n) + "</div>"
        + "<h3>" + esc(it.t) + "</h3><p>" + esc(it.d) + "</p></div>";
    }).join("");
  }
}

/* 动态内容渲染（对比表 / 场景 / 功能分组 / FAQ / 首页集成 / 联系页） */
function renderDynamic() {
  var page = getPageName();
  var dict = (T[page] && T[page][CURRENT_LANG]) || null;
  if (!dict) return;
  if (page === "comparison") renderComparison(dict);
  else if (page === "solutions") renderSolutions(dict);
  else if (page === "features") renderFeatures(dict);
  else if (page === "faq") renderFaq(dict);
  else if (page === "index") renderIndex(dict);
  else if (page === "contact") renderContact(dict);
  else if (page === "pricing") { if (window.__renderPricing) window.__renderPricing(T, CURRENT_LANG); }
  else if (page === "cases") { if (window.__renderCases) window.__renderCases(T, CURRENT_LANG); }
}

/* 应用所有翻译 */
function applyTranslations() {
  /* 更新 <html lang> */
  document.documentElement.lang = CURRENT_LANG;

  /* 更新 <title> */
  var titleEl = document.querySelector("title");
  if (titleEl && titleEl.hasAttribute("data-i18n")) {
    titleEl.textContent = t(titleEl.getAttribute("data-i18n"));
  }

  /* 更新所有带 data-i18n 的 meta（含 description / og:* / twitter:*） */
  document.querySelectorAll("meta[data-i18n]").forEach(function(el) {
    var val = t(el.getAttribute("data-i18n"));
    if (val) el.setAttribute("content", val);
  });

  /* 更新所有 data-i18n 元素 */
  document.querySelectorAll("[data-i18n]").forEach(function(el) {
    var key = el.getAttribute("data-i18n");
    var val = t(key);
    if (val && el.tagName.toLowerCase() !== "meta") el.textContent = val;
  });

  /* 更新所有 data-i18n-html 元素（含 HTML 标签）*/
  document.querySelectorAll("[data-i18n-html]").forEach(function(el) {
    var key = el.getAttribute("data-i18n-html");
    var val = t(key);
    if (val) el.innerHTML = val;
  });

  /* 更新所有 data-i18n-attr 元素（属性翻译）*/
  document.querySelectorAll("[data-i18n-attr]").forEach(function(el) {
    var pairs = el.getAttribute("data-i18n-attr").split(",");
    pairs.forEach(function(pair) {
      var parts = pair.split(":");
      if (parts.length === 2) {
        var val = t(parts[1].trim());
        if (val) el.setAttribute(parts[0].trim(), val);
      }
    });
  });

  /* 渲染动态内容（对比表 / 场景 / 功能分组 / FAQ） */
  renderDynamic();

  /* 通知交互脚本重新观察动态注入的渐入元素 */
  try { window.dispatchEvent(new Event("reveal:refresh")); } catch (e) {}

  /* 更新 hreflang 标签 */
  updateHreflang();

  /* 更新语言切换器当前选项 */
  var switcher = document.getElementById("langSelect");
  if (switcher) switcher.value = CURRENT_LANG;

  /* 通知页面：语言已变更 */
  try {
    document.dispatchEvent(new CustomEvent("lang:changed", { detail: { lang: CURRENT_LANG } }));
  } catch (e) {}

  /* 同步 JSON-LD 结构化数据的描述语言 */
  try {
    var ld = document.getElementById("ldjsonApp");
    if (ld && ld.textContent) {
      var obj = JSON.parse(ld.textContent);
      obj.description = t("page.desc");
      ld.textContent = JSON.stringify(obj);
    }
  } catch (e) {}
}

/* 更新 hreflang alternate 标签 */
function updateHreflang() {
  /* 移除旧的 hreflang 标签 */
  document.querySelectorAll('link[rel="alternate"][hreflang]').forEach(function(el) {
    el.remove();
  });
  /* 添加新的 hreflang 标签 */
  SUPPORTED.forEach(function(lang) {
    var link = document.createElement("link");
    link.rel = "alternate";
    link.hreflang = lang;
    link.href = updateURLLang(lang);
    document.head.appendChild(link);
  });
  /* x-default 指向默认语言 */
  var def = document.createElement("link");
  def.rel = "alternate";
  def.hreflang = "x-default";
  def.href = updateURLLang(DEFAULT_LANG);
  document.head.appendChild(def);
}

/* 更新 URL 中的 lang 参数 */
function updateURLLang(lang) {
  var url = new URL(window.location.href);
  url.searchParams.set("lang", lang);
  return url.toString();
}

/* 切换语言 */
function setLang(lang) {
  if (SUPPORTED.indexOf(lang) < 0) lang = DEFAULT_LANG;
  CURRENT_LANG = lang;
  try { localStorage.setItem("aiops_lang", lang); } catch(e) {}
  /* 更新 URL（不刷新页面）*/
  var url = new URL(window.location.href);
  url.searchParams.set("lang", lang);
  window.history.replaceState({}, "", url.toString());
  applyTranslations();
}

/* 初始化 */
var CURRENT_LANG = detectFromURL() || detectFromStorage() || detectFromBrowser();

/* 注入语言切换器 */
function injectLangSwitcher() {
  var nav = document.querySelector(".nav-inner");
  if (!nav) return;
  /* 检查是否已注入 */
  if (document.getElementById("langSelect")) return;
  /* 创建下拉选择器 */
  var wrap = document.createElement("div");
  wrap.style.cssText = "display:flex;align-items:center;gap:8px;margin-right:4px";
  var select = document.createElement("select");
  select.id = "langSelect";
  select.className = "lang-toggle";
  select.style.cssText = "cursor:pointer;font-family:inherit";
  SUPPORTED.forEach(function(lang) {
    var opt = document.createElement("option");
    opt.value = lang;
    opt.textContent = LANG_NAMES[lang];
    if (lang === CURRENT_LANG) opt.selected = true;
    select.appendChild(opt);
  });
  select.addEventListener("change", function() {
    setLang(this.value);
  });
  wrap.appendChild(select);
  /* 插入到 nav-cta 之前 */
  var cta = nav.querySelector(".nav-cta");
  if (cta) {
    nav.insertBefore(wrap, cta);
  } else {
    nav.appendChild(wrap);
  }
}

/* DOM 就绪后执行（兼容脚本位于 body 内、DOMContentLoaded 已触发或延迟触发等多种情况） */
function init() {
  injectLangSwitcher();
  applyTranslations();
}

function boot() {
  if (document.documentElement.getAttribute("data-i18n-booted")) return;
  document.documentElement.setAttribute("data-i18n-booted", "1");
  init();
}

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot, { once: true });
} else {
  /* 已解析完成（含 DOMContentLoaded 已触发的场景），直接执行，避免漏渲染 */
  boot();
}
/* 兜底：极端情况下若 DOMContentLoaded 已触发而监听器未注册，窗口 load 时再次确保渲染 */
window.addEventListener("load", boot, { once: true });

/* 对外暴露 API（供其他脚本使用） */
window.AIOpsI18n = { _T: T,
  t: t,
  getLang: function () { return CURRENT_LANG; },
  setLang: setLang,
  onLangChange: function (cb) { document.addEventListener("lang:changed", cb); }
};

})();
