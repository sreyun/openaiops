package main

import (
	"compress/gzip"
	"context"
	"flag"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"aiops-monitor/shared"
)

// appVersion is shown in the dashboard sidebar and the summary API.
// The default "AIOps" is a fallback for development builds; production builds
// inject the real Git tag at build time via ldflags:
//
//	go build -ldflags "-X main.appVersion=$(git describe --tags)" ./cmd/server ./cmd/agent
//
// or use the build script:  powershell -File build.ps1
//
// git describe --tags outputs tags like "v3.9.4" (already has the "v" prefix),
// so the frontend renders the value as-is without prepending another "v".
var appVersion = "AIOps"

// resolveDist finds the directory that holds the downloadable agent binaries
// (+ plugins.zip). It tries the -dist flag, ./dist, then the server executable's
// own dir and its dist/ subdir — so the one-line install works whether the
// server is launched from the repo root or from bin/.
func resolveDist(flagVal string) string {
	var candidates []string
	if flagVal != "" {
		candidates = append(candidates, flagVal)
	}
	candidates = append(candidates, "dist")
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(d, "dist"), d)
	}
	for _, c := range candidates {
		if hasAgentBinary(c) {
			return c
		}
	}
	if flagVal != "" {
		return flagVal
	}
	return "dist"
}

var expectedAgentDistNames = []string{
	"aiops-agent.exe",                       // windows/amd64 (legacy name kept for install.ps1 back-compat)
	"aiops-agent-windows-amd64-win2012.exe", // Server 2012/R2 + Win8 (Go 1.20)
	"aiops-agent-windows-arm64.exe",
	"aiops-agent-linux-amd64", "aiops-agent-linux-arm64",
	"aiops-agent-darwin-arm64", "aiops-agent-darwin-amd64",
}

func hasAgentBinary(dir string) bool {
	if dir == "" {
		return false
	}
	for _, n := range expectedAgentDistNames {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

// listMissingAgentDist returns names from expectedAgentDistNames that are absent
// under dir — used at startup so a slim/dev image without macOS/Windows agents
// surfaces a clear warning instead of a cryptic install.sh 404.
func listMissingAgentDist(dir string) []string {
	if dir == "" {
		return append([]string(nil), expectedAgentDistNames...)
	}
	var missing []string
	for _, n := range expectedAgentDistNames {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			missing = append(missing, n)
		}
	}
	return missing
}

// corsMiddleware allows the dashboard (or external tools) to call the API
// cross-origin and short-circuits preflight OPTIONS requests.
// When CORSOrigins is configured, only matching Origin headers are echoed.
// When empty, no Access-Control-Allow-Origin is set (same-origin only) —
// the previous wildcard "*" was removed for enterprise CSRF hardening.
// httpHandler 组装生产环境的中间件洋葱（从外到内）：
//
//	requestID → securityHeaders → CORS → csrfOrigin → gzip → bodyLimit → apiRateLimit → auth → Routes
//
// main 与测试共用这一条链。测试若自己拼一条更短的（只挂 csrf+auth），"线上多出来的那几层
// 把请求挡了"这类故障就永远测不到——真实反馈里的"改分组一直失败"正是死在链上的 csrfOrigin，
// 而不是任何一个 handler 里。
func (s *Server) httpHandler() http.Handler {
	return requestIDMiddleware(securityHeadersMiddleware(s.corsMiddleware(s.csrfOriginMiddleware(
		gzipMiddleware(bodyLimitMiddleware(s.apiRateLimitMiddleware(s.authMiddleware(s.Routes()))))))))
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins := s.cfg.CORSOrigins()
		if len(origins) > 0 {
			origin := r.Header.Get("Origin")
			if origin != "" {
				for _, o := range origins {
					if strings.TrimSpace(o) == origin {
						w.Header().Set("Access-Control-Allow-Origin", origin)
						w.Header().Set("Vary", "Origin")
						w.Header().Set("Access-Control-Allow-Credentials", "true")
						break
					}
				}
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Expose-Headers", "X-AIOps-History-Source")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// maxBodyBytes caps request bodies to blunt memory-exhaustion via oversized
// JSON. Reports (metrics + up to 256 process names + disks + GPUs) fit easily.
// Forwarding proxy requests need a larger limit (up to 100MB for file uploads).
const maxBodyBytes = 100 << 20 // 100 MiB

// bodyLimitMiddleware wraps every request body in a MaxBytesReader so a
// malicious or buggy client can't stream an unbounded payload into memory.
// securityHeadersMiddleware adds conservative hardening headers to every
// response (no MIME sniffing, no framing/clickjacking, no referrer leakage).
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// HSTS：仅在 HTTPS 下发送，强制浏览器后续始终走 TLS，防降级/中间人。HTTP-only 部署不发，
		// 避免把无 TLS 的环境锁死到 https。r.TLS 非空即当前连接为 TLS（含前置于 TLS 反代直连时）。
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		// Content-Security-Policy: defense-in-depth. script-src is 'self' ONLY —
		// all inline on*= handlers were refactored to delegated listeners and the
		// theme-init inline script was externalised (/theme-init.js), so even a
		// stored-XSS payload cannot execute inline JS. style-src keeps 'unsafe-inline'
		// (inline style= attributes are pervasive and low-risk — no script execution).
		// The policy also blocks plugins, base-tag/form hijacking, framing
		// (clickjacking), and cross-origin exfiltration (connect/img/font = self).
		// blob: is required for remote-desktop JPEG/H.264 (createObjectURL frames /
		// MediaSource) and in-browser file download/replay links — without it the
		// browser fires Image.onerror and the UI reports "无法解码的 JPEG 画面".
		// Skipped for /proxy/ — those responses are arbitrary target-host web apps
		// that must keep their own CSP/resources.
		if !strings.HasPrefix(r.URL.Path, "/proxy/") {
			h.Set("Content-Security-Policy",
				"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
					"img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self' data:; connect-src 'self'; "+
					"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		}
		next.ServeHTTP(w, r)
	})
}

// requestIDMiddleware assigns/propagates X-Request-ID for log correlation across
// auth, SRE/AI calls, and audit trails.
type ctxKeyRequestID struct{}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if id == "" || len(id) > 128 {
			id = genToken()
			if len(id) > 32 {
				id = id[:32]
			}
		}
		w.Header().Set("X-Request-ID", id)
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID{}, id))
		next.ServeHTTP(w, r)
	})
}

func requestIDFrom(r *http.Request) string {
	if r == nil {
		return ""
	}
	if v, ok := r.Context().Value(ctxKeyRequestID{}).(string); ok {
		return v
	}
	return r.Header.Get("X-Request-ID")
}

func bodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// gzipWriterPool reuses gzip.Writer instances across requests to avoid per-
// request allocation under the many-host polling load.
var gzipWriterPool = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// gzipResponseWriter transparently compresses the response body. It strips any
// Content-Length (now wrong post-compression) and advertises gzip on the first
// write.
//
// SSE 例外：当 handler 把 Content-Type 设为 text/event-stream 时，本 writer 切换为
// passthrough（直写底层、不压缩）。原因是 gzip.Writer 会把每个 data: 帧压进内部缓冲，
// 直到 Close 才吐给客户端——这会彻底破坏「逐字流式」，让整段 AI 回复一次性到达。
type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wrote       bool
	passthrough bool // true = 命中 SSE：绕过 gzip，直写底层并逐帧 Flush
}

func (w *gzipResponseWriter) ensureHeader() {
	if w.wrote {
		return
	}
	w.wrote = true
	// 流式响应（SSE）必须逐帧实时下发，不能经 gzip 缓冲。
	if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		w.passthrough = true
		return
	}
	h := w.Header()
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
}
func (w *gzipResponseWriter) WriteHeader(code int) {
	// 101/204/304 carry no compressible body — pass through untouched.
	// 206 Partial Content：Content-Range 按未压缩字节计，若再 gzip 会让分片长度与
	// Content-Range 对不上、破坏断点续传，故一并 passthrough（/dl/ 已在中间件层 bypass，
	// 这里是第二道保险，防其它 Range 响应踩坑）。
	if code == http.StatusSwitchingProtocols || code == http.StatusNoContent ||
		code == http.StatusNotModified || code == http.StatusPartialContent {
		// 置 wrote+passthrough：206 带响应体，后续 Write() 若见 !wrote 会再次进入
		// WriteHeader(200) 覆盖掉 206；置位后 Write 直写底层、defer 也不会误 gz.Close()。
		w.wrote = true
		w.passthrough = true
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.ensureHeader()
	w.ResponseWriter.WriteHeader(code)
}
func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.passthrough {
		return w.ResponseWriter.Write(b)
	}
	return w.gz.Write(b)
}

// Flush 实现 http.Flusher —— 这是 SSE 逐字流式能工作的关键：
//  1. gzipResponseWriter 仅内嵌 http.ResponseWriter 接口（该接口不含 Flush），若不显式
//     实现，handler 里的 `w.(http.Flusher)` 断言就会失败、所有 flush 沦为空操作，数据全被
//     憋到 handler 返回。这正是此前 AI 会话/诊断「不逐字」的根因。
//  2. 压缩响应必须先 flush gzip 缓冲、再 flush 底层 writer，否则压缩字节滞留在 gzip 内部；
//     SSE passthrough 则直接 flush 底层。
func (w *gzipResponseWriter) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if !w.passthrough {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// gzipMiddleware compresses text/JSON responses for clients that accept gzip.
// At many-host scale the /hosts + /activity JSON polled every few seconds is the
// dominant bandwidth cost, and it compresses ~8-10x. WebSocket upgrades (the
// remote terminal) are skipped so hijacking still works.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") ||
			strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") ||
			strings.Contains(r.URL.Path, "/terminal") || // WS upgrade + streaming relays must not be buffered
			strings.Contains(r.URL.Path, "/desktop") ||
			strings.Contains(r.URL.Path, "/agent/desktop/") ||
			strings.Contains(r.URL.Path, "/forward") || // port forwarding streams must not be buffered
			strings.HasPrefix(r.URL.Path, "/proxy/") || // HTTP proxy tunnels must not be buffered
			strings.HasPrefix(r.URL.Path, "/dl/") { // 二进制/zip 已是压缩态，再 gzip 无益且会破坏 Range 断点续传
			next.ServeHTTP(w, r)
			return
		}
		gz := gzipWriterPool.Get().(*gzip.Writer)
		gz.Reset(w)
		gzw := &gzipResponseWriter{ResponseWriter: w, gz: gz}
		// 仅当确实用 gzip 压过内容才写 gzip 尾（Close）。SSE passthrough 与空响应跳过，
		// 否则会往流式响应尾部追加乱码字节 / 往 204 等空响应硬塞一段空 gzip。
		defer func() {
			if gzw.wrote && !gzw.passthrough {
				gz.Close()
			}
			gzipWriterPool.Put(gz)
		}()
		next.ServeHTTP(gzw, r)
	})
}

// mustOpenPG connects to PostgreSQL, retrying briefly so a docker-compose cold
// start (PG still initializing behind its healthcheck) doesn't abort the boot.
// There is no embedded fallback: after the retry window a connection failure is
// fatal, by design — the platform stores all relational state in PostgreSQL.
func mustOpenPG(dsn string) *pgStore {
	const attempts = 10
	var lastErr error
	for i := 0; i < attempts; i++ {
		p, err := openPGStore(dsn)
		if err == nil {
			return p
		}
		lastErr = err
		slog.Warn("PostgreSQL 连接未就绪，重试中…", "attempt", i+1, "max", attempts, "err", err)
		time.Sleep(2 * time.Second)
	}
	log.Fatalf("PostgreSQL 连接失败（已重试 %d 次），服务终止：%v", attempts, lastErr)
	return nil
}

func main() {
	addr := flag.String("addr", ":8529", Tz("server.flag_addr"))
	cfgPath := flag.String("config", "server_config.json", Tz("server.flag_config"))
	distDir := flag.String("dist", "", Tz("server.flag_dist"))
	// v5.4.0: admin password reset
	resetAdmin := flag.Bool("reset-admin", false, "Reset the first admin user's password to a random value and print it to console, then exit")
	resetAdminAPI := flag.String("reset-admin-api", "", "Start a local HTTP API on 127.0.0.1:PORT for admin password reset (e.g. -reset-admin-api=:9999)")
	// Emergency MFA unlock when authenticator is lost/desynced (password login still works after this).
	resetAdminMFA := flag.Bool("reset-admin-mfa", false, "Clear MFA/TOTP for the first admin (or -reset-mfa-user), then exit — restart server afterwards")
	resetMFAUser := flag.String("reset-mfa-user", "", "Username for -reset-admin-mfa (default: first admin)")
	// PostgreSQL storage maintenance. Read-only report is safe any time; the
	// reclaim takes ACCESS EXCLUSIVE locks and is therefore never automatic.
	pgReport := flag.Bool("pg-report", false, "Print a PostgreSQL storage/bloat diagnostic (read-only), then exit")
	pgReclaim := flag.Bool("pg-reclaim", false, "One-time VACUUM (FULL, ANALYZE) of bloated tables, then exit — takes ACCESS EXCLUSIVE locks, run in a maintenance window")
	pgReclaimTables := flag.String("pg-reclaim-tables", "", "Comma-separated table list for -pg-reclaim (default: auto-detected bloated tables)")
	flag.Parse()

	// 配置文件支持 JSON 或 YAML/YML：优先用给定路径（存在即用，其扩展名决定解析格式），
	// 否则自动探测同名 .yaml/.yml，方便用户改用更易读的 YAML 配置。
	*cfgPath = shared.ResolveConfigPath(*cfgPath, "server_config.yaml", "server_config.yml")

	// Handle admin password reset flags before any server logic
	if *resetAdmin {
		runResetAdmin(*cfgPath)
		return
	}
	if *resetAdminAPI != "" {
		runResetAdminAPI(*cfgPath, *resetAdminAPI)
		return
	}
	if *resetAdminMFA {
		runResetAdminMFA(*cfgPath, *resetMFAUser)
		return
	}
	if *pgReport {
		runPGReport()
		return
	}
	if *pgReclaim {
		runPGReclaim(*pgReclaimTables)
		return
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	dist := resolveDist(*distDir)
	store := NewStore()

	// Storage is unified on PostgreSQL (all relational data) + VictoriaMetrics (all
	// time-series). The embedded aiops.db single-file store is fully retired — both
	// backends are REQUIRED and the server refuses to start without them, so state
	// can never silently land in a local file.
	dsn := strings.TrimSpace(os.Getenv("AIOPS_POSTGRES_DSN"))
	if dsn == "" {
		log.Fatal("AIOPS_POSTGRES_DSN 未配置：本平台已统一使用 PostgreSQL + VictoriaMetrics 存储，内置数据库已停用。请在环境变量中配置 PostgreSQL DSN（参见 docker-compose.yml）")
	}
	if strings.TrimSpace(os.Getenv("AIOPS_VM_URL")) == "" {
		log.Fatal("AIOPS_VM_URL 未配置：时序数据（指标/趋势）已统一写入 VictoriaMetrics。请在环境变量中配置 VM 地址（参见 docker-compose.yml）")
	}
	// Connect to PostgreSQL with a bounded retry so a docker-compose cold start (PG
	// still initializing) doesn't abort the boot; after the window it is fatal —
	// there is no local fallback.
	pg := mustOpenPG(dsn)
	slog.Info("PostgreSQL 已连接：配置 / 用户 / 审计 / 事件 / 工单 / 会话统一持久化到 PG")
	store.BindPG(pg) // audit log + plugin events → PG
	initSecretKeyStoreFromEnv()
	if secretEncryptionEnabled() {
		slog.Info("配置密钥落库加密已启用（AIOPS_SECRET_KEY）：MFA/SMTP/AI/webhook 等密钥 AES-256-GCM 静态加密")
	} else {
		slog.Warn("未设置 AIOPS_SECRET_KEY：配置中的密钥以明文存库，建议设置以启用静态加密")
	}

	cfg, err := NewConfigStore(*cfgPath, pg)
	if err != nil {
		log.Fatal(err)
	}
	notifier := NewNotifier(store, cfg)
	server := NewServer(store, cfg, notifier, dist, *addr)
	notifier.forward = server.forward
	notifier.hw = server.hw     // 硬件异常接入统一告警链路（去重/推送与 CPU、磁盘等一致）
	notifier.hv = server.hv     // Hyper-V 虚拟机异常接入统一告警链路
	notifier.snmp = server.snmp // SNMP 网络设备异常接入统一告警链路
	notifier.nf = server.nf     // NetFlow 流量异常接入统一告警链路

	server.term.loadRecordings(recordingsDirFor(*cfgPath)) // terminal replays survive restart (file-backed)
	server.desk.setRecDir(filepath.Join(filepath.Dir(recordingsDirFor(*cfgPath)), "desktop-recordings"))
	server.term.pg = pg // 终端会话录制永久留存到 PG（入库审计，不受内存 100 条上限影响）
	server.bindPG(pg)   // load + periodically persist incidents / work orders / sessions
	// 平台自身故障归口：把包级 panic 钩子接到这台 Server（见 self_fault.go）。
	// 必须在拉起任何常驻循环之前装配，否则最早那几次 panic 会漏掉。
	server.bindPlatformFaultSinks()

	go superviseLoop("alert-notifier", func() { notifier.Run(10 * time.Second) })                         // periodic alert evaluation + dedup push
	go superviseLoop("checks", func() { server.checks.Run(5 * time.Second) })                             // custom HTTP/TCP synthetic checks
	go superviseLoop("apimon", func() { server.apimon.Run(5 * time.Second) })                             // API 性能监控：按业务系统批量探测接口
	go superviseLoop("scrapes", func() { server.scrapes.Run(15 * time.Second) })                          // 指标抓取：agentless 抓 exporter 摄入 VM
	go superviseLoop("prom-rules", func() { server.promrules.Run(30 * time.Second) })                     // 指标告警规则：PromQL 评估 → 告警 → incident/AI
	go superviseLoop("playbook-scheduler", func() { server.runScheduler(30 * time.Second) })              // timed playbook triggers (interval/daily/weekly)
	go superviseLoop("slo-evaluator", func() { server.runSLOEvaluator(60 * time.Second) })                // SLO error-budget evaluation → burn incidents
	go superviseLoop("ai-inspection", server.ai.runInspectionLoop)                                        // scheduled AI/heuristic health inspection
	go superviseLoop("duty-report", server.runDutyReportLoop)                                             // daily AI duty morning report → message center
	go superviseLoop("vm-writer", server.vm.run)                                                          // optional VictoriaMetrics remote-write pump
	go superviseLoop("agent-auto-update", func() { server.startAgentAutoUpdateScanner(5 * time.Minute) }) // 周期性扫描在线且版本落后的 agent 主动入队升级
	go superviseLoop("cicd-watcher", func() { server.startCICDFailureWatcher(2 * time.Minute) })          // 勾了「失败告警 / 自动事件」的 CI/CD 连接：红流水线 → 告警 / SRE 事件
	server.initForecastLearn()                                                                            // 预测台账对比实测 → 校准因子 + AI 自学习记忆
	// 启动后自检持久化历史：内存里的 5 分钟环有 30 天，进程活着时会把「VM 里其实
	// 没有数据」完全掩盖，直到下一次发版重启才暴露成「曲线只剩重启之后」。见
	// history_selfcheck.go。
	safeGo("history-selfcheck", func() { server.verifyDurableHistoryAfterStart(time.Now().Unix()) })

	logProductionSecurityBaseline(cfg)
	store.onAudit = server.exportAuditEntry
	handler := server.httpHandler()
	srv := &http.Server{
		Addr:    *addr,
		Handler: handler,
		// ReadHeaderTimeout guards slow-header attacks while leaving request/
		// response bodies unbounded — the terminal relay streams for minutes and
		// the WebSocket is hijacked, so a fixed Read/WriteTimeout can't apply.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown: on SIGINT/SIGTERM, stop accepting new connections,
	// drain active HTTP requests (up to 30s), flush VictoriaMetrics ingest,
	// flush PostgreSQL state, then exit.
	// This replaces the old os.Exit(0) approach which bypassed defer cleanup
	// and forcibly dropped active connections.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("收到停止信号，正在优雅关闭…")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Warn("HTTP 服务关闭异常", "err", err)
		}
		// Drain the VM ingest queue before PG: a SIGTERM used to os.Exit with
		// the last 5s batch still in memory, which is why a "clean" restart
		// punched a hole in every chart. HTTP shutdown has already finished
		// in-flight /agent/report handlers, so this is the last enqueue.
		server.vm.shutdown(vmQueryTimeout() + 2*time.Second)
		// Final flush of all relational state to PostgreSQL, then close cleanly.
		server.pgFlush(pg, true)
		pg.close()
		os.Exit(0)
	}()

	slog.Info(Tz("server.started"))
	// -addr 可以是 ":8529"（只有端口）也可以是 "127.0.0.1:8529"（含主机）。
	// 直接拼 "http://localhost"+addr 在后一种写法下会印出
	// http://localhost127.0.0.1:8529 这种点不开的地址——启动日志是新用户看到的
	// 第一屏，一个拼错的链接很掉印象分。
	base := "http://" + startupDisplayHost(*addr)
	slog.Info(Tz("server.dashboard_url"), "url", base)
	slog.Info(Tz("server.api_url"), "url", base+"/api/v1/")
	slog.Info(Tz("server.config_file"), "path", *cfgPath)
	slog.Info("存储后端", "relational", "PostgreSQL", "timeseries", "VictoriaMetrics", "note", "内置 aiops.db 已停用")
	// 内存态历史环的容量预算：机群一上规模，这一项就是进程内存的大头，而超了之后
	// 只表现为 OOM / 长 GC 停顿，没有任何日志会指向它。启动时直接把账算给运维看。
	logHistoryRingBudget(store.hostCount())
	if hasAgentBinary(dist) {
		slog.Info(Tz("server.dist_dir"), "path", dist, "note", Tz("server.dist_ok"))
		if miss := listMissingAgentDist(dist); len(miss) > 0 {
			slog.Warn("dist 缺少部分平台 Agent，对应系统一键安装会 404",
				"missing", strings.Join(miss, ","),
				"hint", "请用生产 Dockerfile 或已含全平台交叉编译的 Dockerfile.dev 重建 aiops-server")
		}
	} else {
		slog.Warn(Tz("server.dist_missing"))
	}
	// TLS / HTTPS: when a cert+key pair is provided, serve over TLS so agent↔server
	// and browser↔server traffic (login credentials, session cookie, agent
	// fingerprint, terminal I/O) is encrypted. When enabled, isHTTPS(r) becomes true
	// for direct connections, so the session cookie's Secure flag is set automatically.
	// Without it the server still serves plain HTTP (intended only behind a
	// TLS-terminating reverse proxy) and warns loudly.
	certFile := strings.TrimSpace(os.Getenv("AIOPS_TLS_CERT"))
	keyFile := strings.TrimSpace(os.Getenv("AIOPS_TLS_KEY"))
	if certFile != "" && keyFile != "" {
		slog.Info("已启用 TLS/HTTPS（加密传输）", "cert", certFile)
		if err := srv.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
		return
	}
	slog.Warn("未配置 TLS（AIOPS_TLS_CERT/AIOPS_TLS_KEY）：以明文 HTTP 提供服务。生产环境请启用 TLS，或置于 HTTPS 终止代理之后，否则登录凭据/会话/终端数据将明文传输")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// startupDisplayHost turns a listen address into something clickable in the
// startup banner. ":8529" → "localhost:8529"; "0.0.0.0:8529" → "localhost:8529"
// (0.0.0.0 is not routable from a browser); anything else is already a host.
func startupDisplayHost(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "localhost"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port separator (e.g. "8529") — treat the whole thing as a port.
		return "localhost:" + strings.TrimPrefix(addr, ":")
	}
	switch host {
	case "", "0.0.0.0", "[::]", "::":
		host = "localhost"
	}
	return net.JoinHostPort(host, port)
}
