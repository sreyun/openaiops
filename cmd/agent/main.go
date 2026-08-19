package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"aiops-monitor/shared"
)

// ServerConfig represents one backend server target for multi-server push.
// Each entry has its own URL and optional install token; the agent reports
// to all configured servers concurrently (collect once, broadcast all).
type ServerConfig struct {
	Server string `json:"server"`
	Token  string `json:"token,omitempty"`
}

type config struct {
	Server         string         `json:"server"`            // legacy single-server field
	Servers        []ServerConfig `json:"servers,omitempty"` // multi-server: when non-empty, takes precedence over Server+Token
	ReportInterval int            `json:"report_interval"`
	PluginInterval int            `json:"plugin_interval"`
	DiskPath       string         `json:"disk_path"`
	PluginsDir     string         `json:"plugins_dir"`
	Python         string         `json:"python"`
	StateFile      string         `json:"state_file"`
	Category       string         `json:"category"`
	FolderID       string         `json:"folder_id,omitempty"`       // asset-tree node id (any depth); preferred over category for placement
	Token          string         `json:"token"`                     // legacy single-server token
	Relay          bool           `json:"relay"`                     // gateway relay mode: proxy all requests to --server
	Listen         string         `json:"listen,omitempty"`          // relay listen address (e.g. ":8529")
	RelaySecret    string         `json:"relay_secret,omitempty"`    // v5.4.1: shared secret for relay auth
	LogPaths       []string       `json:"log_paths,omitempty"`       // log files/dirs to tail and forward to the server
	LogEncrypt     bool           `json:"log_encrypt"`               // gzip+AES-256-GCM encrypt log uploads (default true)
	TLSSkipVerify  bool           `json:"tls_skip_verify,omitempty"` // skip server TLS cert verification (insecure; self-signed/lab only)
	CACert         string         `json:"ca_cert,omitempty"`         // path to a CA PEM bundle to trust (proper self-signed support)
	// ---- 新增采集器配置（可选，未配置时不启动）----
	RedfishTargets []RedfishTarget `json:"redfish_targets,omitempty"` // Redfish 硬件状态采集（服务器 BMC/iDRAC/iBMC）
	// OceanStor 不支持 Redfish，必须走 DeviceManager REST，因此是独立配置项
	OceanStorTargets []OceanStorTarget `json:"oceanstor_targets,omitempty"` // 华为 OceanStor 存储/磁盘框采集
	NetFlow          *NetFlowConfig    `json:"netflow,omitempty"`           // NetFlow 网络流量接收
	PacketCapture    *PacketConfig     `json:"packet_capture,omitempty"`    // 五元组包报文采集
	SNMP             *SNMPConfig       `json:"snmp,omitempty"`              // SNMP 轮询 + Trap 接收（网络设备纳管）
	SNI              *SNIConfig        `json:"sni_dns_capture,omitempty"`   // 跨平台 DNS/SNI + 受控明文 HTTP 审计（默认关）
	// Hyper-V 虚拟机采集：默认在 Windows Hyper-V 宿主机上自动探测启用，无需配置
	HyperVIntervalSec int  `json:"hyperv_interval_sec,omitempty"` // 采集间隔(秒)，默认 60
	HyperVDisabled    bool `json:"hyperv_disabled,omitempty"`     // 显式关闭 Hyper-V 采集
	// Docker/Podman 容器清单：有 CLI 时自动采集
	ContainerIntervalSec int  `json:"container_interval_sec,omitempty"`
	ContainerDisabled    bool `json:"container_disabled,omitempty"`
}

func defaultConfig() config {
	py := "python3"
	if runtime.GOOS == "windows" {
		py = "python"
	}
	return config{
		Server: "http://localhost:8529",
		// 默认 30s/60s：10s/15s 对生产车队过于激进（每小时 360 次全量上报 + 每分钟数十次
		// 插件冷启动）。30s 是主流监控采样粒度（Prometheus/Zabbix 同量级），带宽降 3×、
		// 插件 spawn 降 4×。需要更实时的用户可在配置里下调 report_interval/plugin_interval。
		ReportInterval: 30,
		PluginInterval: 60,
		DiskPath:       defaultDiskPath(),
		PluginsDir:     "plugins",
		Python:         py,
		StateFile:      "agent_state.json",
		Category:       "",
		Token:          "",
		Listen:         ":8529",
		LogEncrypt:     true, // 日志加密上报默认开启
	}
}

func defaultDiskPath() string {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("SystemDrive"); d != "" {
			return d + "\\"
		}
		return "C:\\"
	}
	return "/"
}

// valueTakingAgentFlags are the flags whose *next* argv token is a value, not a
// flag. Needed so `--config --version` (a path literally named "--version") is
// not mistaken for a version request during the pre-config scan.
var valueTakingAgentFlags = map[string]bool{
	"-server": true, "-interval": true, "-plugin-interval": true, "-disk-path": true,
	"-plugins-dir": true, "-python": true, "-category": true, "-folder-id": true,
	"-token": true, "-listen": true, "-relay-secret": true, "-config": true,
	"-log-paths": true, "-ca-cert": true, "-security-mode": true,
}

// argsRequestVersion reports whether argv asks for `--version` / `-version`.
// Mirrors the flag package's accepted spellings (both dash forms, optional
// `=value`) and stops at the first non-flag argument, like flag.Parse does.
func argsRequestVersion(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || a == "" || a[0] != '-' || a == "-" {
			return false
		}
		name, val, hasVal := strings.Cut(a, "=")
		norm := "-" + strings.TrimLeft(name, "-")
		if norm == "-version" {
			// `--version=false` explicitly opts out.
			return !hasVal || (val != "false" && val != "0")
		}
		if !hasVal && valueTakingAgentFlags[norm] {
			i++ // skip the separate value token
		}
	}
	return false
}

func main() {
	// --version must stay a pure, silent, side-effect-free probe: the self-update
	// helpers run `<staged binary> --version` BEFORE swapping the live agent to
	// prove the download actually starts on this kernel. Handling it here — ahead
	// of config load, slog output and ensureConfigExample — matters because:
	//   1. a single stderr line makes the PowerShell helper's `& $new --version`
	//      throw under $ErrorActionPreference='Stop' (native stderr → terminating
	//      NativeCommandError), which aborted every Windows upgrade before the swap;
	//   2. a malformed config.yaml in CWD would log.Fatalf here and mark a
	//      perfectly good binary "not runnable";
	//   3. ensureConfigExample would litter config.example.yaml into the probe's
	//      CWD (C:\Windows\System32 for the SYSTEM scheduled task).
	// AIOPS_UPDATE_PROBE=1 is set by the updater and forces the same fast path.
	if argsRequestVersion(os.Args[1:]) || os.Getenv("AIOPS_UPDATE_PROBE") == "1" {
		fmt.Println(agentVersion())
		return
	}
	// Prefer WriteConsoleW on an attached Windows console so UTF-8 Chinese slog
	// lines are not visually duplicated under CP 65001 WriteFile quirks.
	slog.SetDefault(slog.New(newAgentTextHandler(shared.NewConsoleAwareWriter(os.Stderr))))
	// LocalSystem services often inherit a truncated/empty Path; repair once so
	// every child (remote terminal, playbooks) can resolve ipconfig/chcp/…
	ensureWindowsProcessPath()

	cfg := defaultConfig()

	// resolve config file path (manual scan so we can load before flag defaults)。
	// 默认自动探测 config.yaml / config.yml / config.json（第一个存在者）；YAML 为推荐格式，
	// 优先级最高，故新旧安装并存时（迁移期）以 YAML 为准。--config 显式指定则优先，
	// 且按其扩展名决定 JSON/YAML 解析。
	cfgPath := shared.ResolveConfigPath(agentConfigCandidates()...)
	for i, a := range os.Args {
		if a == "--config" && i+1 < len(os.Args) {
			cfgPath = os.Args[i+1]
		}
	}
	// 显式给了 --config 却指到一个不存在的文件时，退回二进制旁边那份配置。
	//
	// 这不是防御性编程，是修一次现网事故：升级助手用
	// `Start-Process -ArgumentList @('--install-service','--config',$Cfg)` 拉起 Agent，而
	// Start-Process 把数组用空格拼接、**不加任何引号**，于是默认安装路径
	// `C:\Program Files\AIOps Agent\config.yaml` 到达 Agent 时变成了 `--config C:\Program`。
	// Agent 把这个残缺路径原样写进服务 ImagePath，此后每一次启动都读不到配置、退回
	// localhost:8529——服务状态正常、进程活着、二进制是最新的，而主机在控制台上永远离线。
	// 一次**成功**的换版就这样把机器打没了。
	//
	// 助手侧的引号已经补上，但已经被写坏 ImagePath 的存量主机不会自己好：它们连不上
	// 服务端，收不到任何修复。这一段让它们在换上带此修复的二进制之后自行认回配置。
	// 只在旁边确实有配置时才退回，且必须留下 WARN——静默替换配置比读不到配置更危险。
	if cfgPath != "" {
		if _, err := os.Stat(cfgPath); err != nil {
			if beside := firstExistingBesideExe(); beside != "" {
				slog.Warn("--config 指定的文件不存在，改用可执行文件旁边的配置",
					"given", cfgPath, "used", beside,
					"hint", "服务 ImagePath 里的 --config 很可能被空格截断，请以管理员重新注册服务：aiops-agent --install-service --config \"<安装目录>\\config.yaml\"")
				cfgPath = beside
			}
		}
	}
	// Remember the config this process actually runs with: the Windows/Linux
	// update helpers must relaunch the new binary with the SAME --config, and it
	// is not necessarily the one sitting beside the executable.
	if abs, err := filepath.Abs(cfgPath); err == nil {
		agentActiveConfigPath = abs
	} else {
		agentActiveConfigPath = cfgPath
	}
	// Load configuration: file-not-found is expected on first manual run, but
	// parse errors MUST surface — a silently-failed parse would leave the
	// agent pointing at the hardcoded default (localhost:8529), which is the
	// #1 cause of "agent reports to localhost" on freshly-installed Linux hosts
	// where the install script exited before writing config.
	// 日志里一律写**绝对**路径。这次现场排查卡了很久，就是因为这一行原来打印的是
	// "path=config.yaml"——一个相对路径，看不出它到底解析到了哪里，而"到底解析到了哪里"
	// 正是全部答案（服务的工作目录是 System32，不是安装目录）。
	cfgFound := false
	if b, err := os.ReadFile(cfgPath); err == nil {
		cfgFound = true
		if err := shared.DecodeConfig(cfgPath, b, &cfg); err != nil {
			log.Fatalf("配置文件解析失败，已拒绝使用默认 localhost 配置继续运行: path=%s err=%v", agentActiveConfigPath, err)
		} else {
			slog.Info("已加载配置文件", "path", agentActiveConfigPath)
		}
	} else if os.Getenv("AIOPS_SERVER") == "" {
		wd, _ := os.Getwd()
		slog.Warn("配置文件不存在，使用默认配置（localhost:8529）",
			"path", agentActiveConfigPath, "cwd", wd,
			"hint", "请运行安装命令生成 config.yaml，或使用 --config / AIOPS_SERVER 指定服务端")
	} else {
		slog.Info("配置文件不存在，使用环境变量 AIOPS_SERVER", "path", agentActiveConfigPath)
	}

	// 首次启动时在配置目录自动生成 config.example.yaml（已存在则跳过）
	ensureConfigExample(cfgPath)

	// flags override file/defaults
	var cfgFlag string
	flag.StringVar(&cfg.Server, "server", cfg.Server, "服务端地址，如 http://192.168.1.10:8529")
	flag.IntVar(&cfg.ReportInterval, "interval", cfg.ReportInterval, "基础指标上报间隔(秒)")
	flag.IntVar(&cfg.PluginInterval, "plugin-interval", cfg.PluginInterval, "插件执行周期(秒)")
	flag.StringVar(&cfg.DiskPath, "disk-path", cfg.DiskPath, "监控的磁盘路径")
	flag.StringVar(&cfg.PluginsDir, "plugins-dir", cfg.PluginsDir, "Python 插件目录")
	flag.StringVar(&cfg.Python, "python", cfg.Python, "运行 .py 插件的解释器")
	flag.StringVar(&cfg.Category, "category", cfg.Category, "主机分类标签，如 生产/测试/DB/办公终端")
	flag.StringVar(&cfg.FolderID, "folder-id", cfg.FolderID, "主机资产树分组 ID（任意深度节点，优先于 category）")
	flag.StringVar(&cfg.Token, "token", cfg.Token, "安装 Token（由服务端安装命令注入，可选）")
	flag.BoolVar(&cfg.Relay, "relay", cfg.Relay, "网关中继模式：监听本地端口，转发所有请求到 --server 指定的云监控中心")
	flag.StringVar(&cfg.Listen, "listen", cfg.Listen, "Relay 监听地址，如 :8529")
	flag.StringVar(&cfg.RelaySecret, "relay-secret", cfg.RelaySecret, "Relay 共享密钥，用于上游服务端验证中继请求")
	flag.StringVar(&cfgFlag, "config", cfgPath, "配置文件路径")
	var logPathsFlag string
	flag.StringVar(&logPathsFlag, "log-paths", "", "采集的日志文件或目录路径，逗号分隔（如 /var/log/syslog,/var/log/nginx/）")
	flag.BoolVar(&cfg.LogEncrypt, "log-encrypt", cfg.LogEncrypt, "加密上报日志(gzip+AES-256-GCM)，默认开启；调试可设 --log-encrypt=false")
	flag.BoolVar(&cfg.TLSSkipVerify, "tls-skip-verify", cfg.TLSSkipVerify, "跳过服务端 TLS 证书校验（不安全，仅自签/内网临时使用）")
	flag.StringVar(&cfg.CACert, "ca-cert", cfg.CACert, "信任的 CA 证书路径（PEM），用于校验自签名服务端证书")
	var securityMode string
	flag.StringVar(&securityMode, "security-mode", "auto", "安全模块模式: auto(自动诊断输出修复命令)/permissive(自动切换宽容模式,2h后恢复)/enforcing(恢复强制模式)")
	// Privileged root-daemon / session desktop-worker controls (Windows SCM,
	// Linux systemd, macOS LaunchDaemon).
	var svcInstall, svcUninstall, svcRun, desktopWorker, sendSASOnce bool
	flag.BoolVar(&svcInstall, "install-service", false, "以 root/SYSTEM 权限安装并启动 Agent 守护进程（Windows 服务 / Linux systemd / macOS LaunchDaemon；远程桌面支持锁屏/登录及开机自启所需）")
	flag.BoolVar(&svcUninstall, "uninstall-service", false, "停止并卸载 Agent 守护进程")
	flag.BoolVar(&svcRun, "service", false, "内部使用：由服务管理器（SCM/systemd/launchd）以守护进程方式启动")
	flag.BoolVar(&desktopWorker, "desktop-worker", false, "内部使用：由守护进程派生、运行于活动图形会话的远程桌面 worker")
	flag.BoolVar(&sendSASOnce, "send-sas", false, "内部使用：在目标会话内注入一次 Ctrl+Alt+Del（Windows Server 锁屏兼容）")
	var selfTest bool
	flag.BoolVar(&selfTest, "selftest", false, "安装自检：验证 DNS/TCP/TLS 与服务端注册握手，失败时给出具体原因并返回非 0")
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "打印 Agent 版本并退出")
	flag.Parse()

	if showVersion {
		fmt.Println(agentVersion())
		return
	}

	if sendSASOnce {
		if err := runSendSASOnce(); err != nil {
			log.Fatalf("send-sas 失败: %v", err)
		}
		return
	}

	// Service install/uninstall are one-shot admin actions handled before the
	// (config-heavy) agent bootstrap. Install embeds the resolved absolute config
	// path so the SYSTEM service — whose CWD is system32 — finds it.
	if svcInstall || svcUninstall {
		if svcUninstall {
			if err := uninstallAgentService(); err != nil {
				log.Fatalf("卸载服务失败: %v", err)
			}
			slog.Info("Agent 服务已卸载")
			return
		}
		exe, err := os.Executable()
		if err != nil {
			log.Fatalf("无法解析可执行文件路径: %v", err)
		}
		absCfg := cfgPath
		if p, e := filepath.Abs(cfgPath); e == nil {
			absCfg = p
		}
		if err := installAgentService(exe, absCfg); err != nil {
			log.Fatalf("安装服务失败: %v", err)
		}
		slog.Info("Agent 守护进程已安装并启动（root/SYSTEM，开机自启）", "config", absCfg)
		return
	}
	explicitFlags := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })
	// An explicit single-server flag is an override, not a no-op behind a
	// servers[] value loaded from the file.
	if explicitFlags["server"] {
		cfg.Servers = nil
	}
	_ = cfgFlag
	if logPathsFlag != "" {
		for _, p := range strings.Split(logPathsFlag, ",") {
			if p = strings.TrimSpace(p); p != "" {
				cfg.LogPaths = append(cfg.LogPaths, p)
			}
		}
	}

	// Environment variable overrides (lowest priority: flag > env > config file > default).
	// Enables container / Kubernetes deployments where secrets are injected via env.
	if v := os.Getenv("AIOPS_SERVER"); v != "" && !explicitFlags["server"] {
		cfg.Server = v
		cfg.Servers = nil
	}
	if v := os.Getenv("AIOPS_TOKEN"); v != "" && !explicitFlags["token"] {
		cfg.Token = v
	}
	// Compose/K8s: read install token from a file the server publishes
	// (e.g. /app/server-data/.install_token). Env AIOPS_TOKEN still wins when set.
	if cfg.Token == "" && !explicitFlags["token"] {
		if fp := strings.TrimSpace(os.Getenv("AIOPS_TOKEN_FILE")); fp != "" {
			if b, err := os.ReadFile(fp); err == nil {
				if tok := strings.TrimSpace(string(b)); tok != "" {
					cfg.Token = tok
				}
			}
		}
	}
	if v := os.Getenv("AIOPS_INTERVAL"); v != "" && !explicitFlags["interval"] {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ReportInterval = n
		}
	}
	if v := os.Getenv("AIOPS_CATEGORY"); v != "" && !explicitFlags["category"] {
		cfg.Category = v
	}
	if v := os.Getenv("AIOPS_FOLDER_ID"); v != "" && !explicitFlags["folder-id"] {
		cfg.FolderID = v
	}
	if v := os.Getenv("AIOPS_PLUGINS_DIR"); v != "" && !explicitFlags["plugins-dir"] {
		cfg.PluginsDir = v
	}
	if v := os.Getenv("AIOPS_STATE_FILE"); v != "" {
		cfg.StateFile = v
	}
	if v := os.Getenv("AIOPS_LOG_ENCRYPT"); v != "" && !explicitFlags["log-encrypt"] {
		cfg.LogEncrypt = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("AIOPS_TLS_SKIP_VERIFY"); v != "" && !explicitFlags["tls-skip-verify"] {
		cfg.TLSSkipVerify = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("AIOPS_CA_CERT"); v != "" && !explicitFlags["ca-cert"] {
		cfg.CACert = v
	}
	if v := os.Getenv("AIOPS_LISTEN"); v != "" && !explicitFlags["listen"] {
		cfg.Listen = v
	}
	if v := os.Getenv("AIOPS_RELAY_SECRET"); v != "" && !explicitFlags["relay-secret"] {
		cfg.RelaySecret = v
	}
	if err := normalizeAndValidateConfig(&cfg); err != nil {
		log.Fatalf("Agent 配置校验失败: path=%s err=%v", agentActiveConfigPath, err)
	}
	// The working directory belongs to whoever started us (the Windows SCM uses
	// System32), so relative paths must be anchored to the install dir instead.
	resolveConfigRelativePaths(&cfg, cfgPath)
	// Mirror slog into the install directory on every OS. Services / hidden
	// consoles discard stderr; journald/launchd alone is easy to miss on site.
	// Retention: 7 × 10 MiB rolling files under the config/install directory.
	{
		name := "agent.log"
		if desktopWorker {
			name = "agent-desktop.log"
		}
		startServiceFileLog(agentLogBaseDir(cfgPath, cfgFound), name)
	}
	// 放在 startServiceFileLog 之后：Fatalf 的原因必须落进安装目录的 agent.log。
	// Windows 服务的 stderr 没有任何去处，先炸再写日志等于什么都没说。
	// 服务模式下「读不到配置」必须当场炸掉，绝不能带着默认的 localhost:8529 继续跑。
	//
	// 现场故障就长这样：服务状态正常、进程活着、二进制是最新的、重启一百次也一样，而
	// 控制台上主机永远离线——因为它一直在向 localhost 上报。唯一的线索是一条 WARN，而
	// 那条 WARN 跟着 cfgPath 一起落进了 C:\Windows\System32\agent.log，没人会去那里找。
	// 一个"看起来健康的哑巴"比一个起不来的服务坏得多：后者 SCM 会记录、会重试、人一眼
	// 就能看见。手工前台运行不受此限（第一次跑还没配置是正常的）。
	if svcRun && !cfgFound && os.Getenv("AIOPS_SERVER") == "" && !explicitFlags["server"] {
		wd, _ := os.Getwd()
		log.Fatalf("服务模式下找不到配置文件，拒绝以默认 localhost:8529 继续运行（那会让本机在控制台上永远离线）: "+
			"path=%s cwd=%s；请以管理员重新注册服务并带上绝对路径的 --config："+
			`aiops-agent --install-service --config "<安装目录>\config.yaml"`, agentActiveConfigPath, wd)
	}

	// Apply server TLS trust (self-signed CA / skip-verify) to every agent→server
	// HTTP client before the first request is made.
	configureServerTLS(cfg.TLSSkipVerify, cfg.CACert)

	// Relay mode: act as a gateway for internal machines that can't reach the
	// internet. The agent listens on a local port and reverse-proxies to the
	// cloud server — only this one machine needs internet access.
	if cfg.Relay {
		listen := cfg.Listen
		if listen == "" {
			listen = ":8529"
		}
		// The gateway is a managed host like any other — it keeps collecting and
		// reporting to the upstream while it relays, so it shows up in the host
		// list and can be upgraded, inspected and opened as a terminal.
		//
		// 这里以前是 runRelay(...) + return：内网每一台 agent 都吊在这台机器上，而它
		// 恰恰是面板上唯一看不见的一台——没有指标、没有告警、自动升级够不到它，出事
		// 只能有人登上去看。它出网正常（这是中继模式的前提），直连上游上报没有额外
		// 成本；上报走 cfg.Server，不绕自己一圈。
		// 回源地址要和上报地址走同一次 http→https 升级，见 resolveRelayUpstream。
		// 放进 goroutine 是为了不让探测（最坏 ~15s）挡住启动的其余部分。
		go runRelay(listen, resolveRelayUpstream(cfg.Server), cfg.RelaySecret)
		if strings.TrimSpace(cfg.Token) == "" {
			// 老网关（本改动之前装的）配置里没有 token。服务端开了安装 Token 校验时
			// 注册会被拒，症状是"中继照常工作、主机列表里就是没有它"——说清楚比让人
			// 去翻 403 日志强。
			slog.Warn("中继网关未配置 token：若服务端开启了安装 Token 校验，本机不会出现在主机列表",
				"fix", "用面板「安装 Agent → 网关中继」里带 ?token= 的命令重跑一次安装即可（配置与缓存都保留）")
		}
	}

	// Desktop workers must NEVER mint a new host_id. The service owns identity
	// reconciliation; a worker that races loadOrCreateHostID during a brief
	// state-file gap can persist a competing id and leave deskWait on a host
	// the UI never rings (Win11 "remote desktop connected / black / timeout").
	var hostID string
	if desktopWorker {
		hostID = waitHostIDFromState(cfg.StateFile, 20*time.Second)
		if hostID == "" {
			log.Fatalf("桌面 worker 无法读取 agent_state.json 中的 host_id（path=%s）；请确认 AiopsMonitorAgent 服务已启动并完成身份同步", cfg.StateFile)
		}
	} else {
		hostID = loadOrCreateHostID(cfg.StateFile)
	}
	setFIMStateDir(cfg.StateFile)
	collector := newCollector(cfg.DiskPath)
	runner := NewPluginRunner(cfg.PluginsDir, cfg.Python, 15*time.Second)

	// v5.4.0: 安全环境检测（麒麟 kysec / SELinux / AppArmor / firewalld / Defender / SIP）
	// 启动时主动探测并输出诊断信息，让运维人员第一时间看到安全模块拦截风险。
	// 输出检测到的 OS 发行版信息
	osDist := getOSDist()
	if osDist.PrettyName != "" {
		slog.Info("检测到操作系统", "distro", osDist.PrettyName, "id", osDist.ID, "version", osDist.Version)
	} else if osDist.Name != "" {
		slog.Info("检测到操作系统", "name", osDist.Name, "id", osDist.ID, "version", osDist.Version)
	}

	if secModules, isKylin := detectSecurityEnv(); isKylin || len(secModules) > 0 {
		if isKylin {
			slog.Warn("检测到麒麟操作系统，请确认 kysec 安全模块不会拦截 Agent 数据采集",
				"os", runtime.GOOS, "distro", osDist.PrettyName)
		}
		var enforcingModules []SecurityModule
		for _, m := range secModules {
			level := slog.LevelInfo
			if m.Status == "enforcing" {
				level = slog.LevelWarn
				enforcingModules = append(enforcingModules, m)
			}
			slog.Log(context.Background(), level, "检测到安全模块",
				"module", m.Name, "status", m.Status, "details", m.Details)
		}
		// Handle --security-mode parameter
		switch securityMode {
		case "permissive":
			if len(enforcingModules) > 0 {
				slog.Warn("安全模式=permissive，正在切换安全模块为宽容模式（2小时后自动恢复）")
				if err := setKysecMode("permissive", 2*time.Hour); err != nil {
					slog.Error("切换安全模块失败", "err", err)
				} else {
					slog.Info("安全模块已切换为宽容模式，2小时后自动恢复 enforcing")
				}
			}
		case "enforcing":
			slog.Info("安全模式=enforcing，正在恢复安全模块强制模式")
			if err := setKysecMode("enforcing", 0); err != nil {
				slog.Error("恢复安全模块失败", "err", err)
			} else {
				slog.Info("安全模块已恢复为 enforcing 模式")
			}
		case "auto":
			// Auto mode: output fix commands for any enforcing modules
			if len(enforcingModules) > 0 {
				cmds := securityFixCommands(enforcingModules)
				if len(cmds) > 0 {
					slog.Warn("检测到 enforcing 安全模块，以下是推荐的修复命令：")
					for _, cmd := range cmds {
						slog.Warn("  " + cmd)
					}
				}
			}
		}
		// Proactively check if procfs access is blocked
		if blocked := checkProcAccess(); len(blocked) > 0 {
			var paths []string
			for p := range blocked {
				paths = append(paths, p)
			}
			slog.Error("启动检测：部分 /proc 路径无法读取，数据采集可能不完整",
				"blocked_paths", paths,
				"hint", "请以 root 身份运行 Agent，或配置安全模块白名单",
			)
		}
	}

	// Resolve the effective server list: if "servers" is configured it takes
	// precedence; otherwise fall back to the legacy single "server" + "token".
	servers := cfg.Servers
	if len(servers) == 0 && cfg.Server != "" {
		servers = []ServerConfig{{Server: cfg.Server, Token: cfg.Token}}
	}
	if len(servers) == 0 {
		log.Fatal("未配置任何服务端地址（--server 或 servers 字段）")
	}
	// Behind reverse proxies that 301 http→https, streaming terminal/desktop TX
	// cannot replay Pipe bodies across redirects. Upgrade http://host → https://host
	// up-front when the server advertises that redirect (and persist into config).
	servers = normalizeServersPreferHTTPS(servers, cfgPath)
	if len(servers) > 0 && strings.TrimSpace(servers[0].Server) != "" {
		cfg.Server = servers[0].Server
	}
	// Guard: detect localhost target — a freshly-installed remote agent
	// connecting to its OWN localhost is the most common misconfiguration.
	// This typically means the config file was never written (install script failed
	// partway through, or the agent binary was copied without running the
	// install command). Relay mode is exempt: it listens locally by design.
	if !cfg.Relay {
		for _, sc := range servers {
			if strings.Contains(sc.Server, "localhost") || strings.Contains(sc.Server, "127.0.0.1") {
				slog.Error("Agent 上报地址为本地回环地址，远程连接必然失败！",
					"server", sc.Server,
					"config_path", cfgPath,
					"hint", "config.yaml 可能未正确生成。请在面板重新生成安装命令并执行，或手动编辑 config.yaml 的 server 字段为服务端实际可达地址")
			}
		}
	}
	// Log effective server(s) at startup for quick diagnosis
	for _, sc := range servers {
		slog.Info("Agent 上报目标", "server", sc.Server, "config_path", cfgPath)
	}
	if selfTest {
		os.Exit(runSelfTest(os.Stdout, servers, hostID, cfgPath, cfg.StateFile))
	}
	// Linux: rewrite legacy sandboxed / non-root units so remote terminal can
	// write /etc and $HOME (vim E45 / ProtectHome). No-op on other platforms.
	//
	// 中继网关跳过：它装的是 aiops-relay.service（或用户态 nohup/VBS），而这段在非 root
	// 下会 sudo --install-service 把自己重装成 aiops-agent 服务再 os.Exit(0)。对一台内网
	// 所有 agent 都吊在上面的网关机，开机就换掉服务名、顺带重启一次，代价远大于"远程
	// 终端能不能写 /etc"这点收益。
	if !cfg.Relay {
		ensureLinuxAgentUnitPrivileges(cfgPath)
	} else {
		slog.Info("中继网关：跳过 systemd 单元提权自愈（不改动 aiops-relay 服务），仅上报与远程能力照常")
	}
	agent := NewAgent(
		servers,
		time.Duration(cfg.ReportInterval)*time.Second,
		time.Duration(cfg.PluginInterval)*time.Second,
		collector, runner, hostID, cfg.Category,
	)
	agent.identity.FolderID = strings.TrimSpace(cfg.FolderID)
	agent.logPaths = cfg.LogPaths
	agent.logEncrypt = cfg.LogEncrypt
	agent.stateFile = cfg.StateFile // 认回规范 host_id 后要写回身份文件
	agent.redfishTargets = cfg.RedfishTargets
	agent.oceanStorTargets = cfg.OceanStorTargets
	agent.netflowCfg = cfg.NetFlow
	agent.packetCfg = cfg.PacketCapture
	agent.snmpCfg = cfg.SNMP
	agent.sniCfg = cfg.SNI
	agent.hypervInterval = time.Duration(cfg.HyperVIntervalSec) * time.Second
	agent.hypervDisabled = cfg.HyperVDisabled
	agent.containerInterval = time.Duration(cfg.ContainerIntervalSec) * time.Second
	agent.containerDisabled = cfg.ContainerDisabled

	// Windows secure-desktop worker: run ONLY the remote-desktop channel, with
	// capture/input following the input desktop (lock/login screens). Spawned by
	// the service into the active console session.
	if desktopWorker {
		if err := runDesktopWorker(agent); err != nil {
			log.Fatalf("桌面 worker 运行失败: %v", err)
		}
		return
	}
	// Windows service mode: run the full agent (desktop channel delegated to a
	// worker) under the Service Control Manager.
	if svcRun {
		if err := runAgentAsService(agent, cfgPath); err != nil {
			log.Fatalf("服务运行失败: %v", err)
		}
		return
	}

	// Graceful shutdown: cancel context on SIGTERM/SIGINT, then wait for all
	// goroutines (report loop, plugin loop, terminal/forward channels, hardware
	// collectors) to drain before exiting. This prevents data loss on in-flight
	// reports and ensures the server sees a clean disconnect.
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		select {
		case <-sig:
			slog.Info("收到退出信号，正在优雅停止...")
			cancel()
		case <-ctx.Done():
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		agent.Run(ctx)
	}()

	wg.Wait()
	slog.Info("Agent 已完全停止。")
}
