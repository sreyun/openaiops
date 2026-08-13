package main

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed all:web
var webFS embed.FS

// Server wires the store, the operator-editable config and the notifier to
// HTTP handlers.
type Server struct {
	store     *Store
	cfg       *ConfigStore
	notifier  *Notifier
	auth      *Auth
	checks    *checkRunner
	apimon    *apiRunner          // API 性能监控：按业务系统批量探测接口
	scrapes   *scrapeManager      // 指标抓取（agentless exporter 摄入 Prometheus 生态）
	promrules *promRuleManager    // 指标告警规则（PromQL）
	term      *termManager        // remote terminal relay
	desk      *deskManager        // web remote desktop (agent screen stream)
	forward   *forwardManager     // port forwarding relay (TCP + HTTP proxy)
	emailMgr  *emailManager       // verification codes + reset tokens
	playbooks *playbookManager    // automation playbooks + execution history
	inspect   *hostInspectManager // deep host inspect batches (host_inspect)
	push      *pushHub            // P3-1: WebSocket push hub for real-time updates
	// --- SRE workflow layer ---
	incidents    *incidentManager         // incident hub (alert/SLO/manual)
	remediation  *remediationManager      // closed-loop auto-remediation
	slos         *sloManager              // SLO + error budgets
	distProbes   *distProbeManager        // 分布式多点探测（迭代 D）
	tickets      *ticketManager           // work orders
	oncall       *onCallManager           // on-call escalation pages
	changes      *changeManager           // change records
	logs         *logStore                // aggregated agent logs
	hw           *hardwareStore           // latest Redfish snapshots per host (feeds hardware alerts)
	hv           *hypervStore             // latest Hyper-V guest inventory per host (feeds VM alerts)
	snmp         *snmpStore               // latest SNMP device snapshots per host (feeds SNMP alerts)
	nf           *nfStore                 // per-host NetFlow window stats + baseline (feeds traffic-anomaly alerts)
	ai           *aiManager               // AI inspection + diagnosis
	vm           *vmWriter                // optional VictoriaMetrics remote-write
	messages     *messageHub              // unified notification center (SRE/alert/AI feed)
	distDir      string                   // directory of downloadable agent binaries + plugins.zip
	pg           *pgStore                 // PostgreSQL persistence (optional, for pgvector/RAG)
	sreyun       *SreyunCore              // Sreyun Agent (autonomous SRE agent)
	mcpClients   *MCPClientManager        // external MCP Servers bridged into Sreyun tools
	aiStats      *aiStatsHub              // AI 调用观测（延迟/失败率/粗估 token，管理页仪表）
	aiGov        *aiGovHub                // AI 治理：配额 + 写工具审计
	assistStore  *assistStore             // Assist 服务端原文（反馈防投毒）
	hostSec      *hostSecurityManager     // 主机安全扫描结果
	webSec       *webScanManager          // Web Nuclei 扫描结果
	feeds        *feedManager             // 威胁情报/模板库更新（Nuclei 模板、sqlmap 特征等）
	sqlChanges   *sqlChangeRequestManager // SQL DDL approval tickets
	sqlHistory   *sqlQueryHistoryManager  // per-user SQL workbench history (full SQL)
	sqlSlow      *slowSQLManager          // multi-DB slow SQL digests + advice
	secFindings  *securityFindingManager  // security finding lifecycle states
	agentUpdates *agentUpdateManager      // fleet agent binary update jobs
	cicdWatcher  *cicdFailureWatcher      // CI/CD failed-run alert / auto-incident state
	// --- AI 记忆异步写入通道 ---
	memoryCh  chan memoryJob // 异步记忆写入队列
	memorySem chan struct{}  // Embedding API 并发信号量（最多 3 并发）
	memoryWg  sync.WaitGroup // 等待 worker 排空
}

func NewServer(store *Store, cfg *ConfigStore, notifier *Notifier, distDir string, selfAddr string) *Server {
	s := &Server{
		store: store, cfg: cfg, notifier: notifier, distDir: distDir,
		auth:         NewAuth(cfg),
		checks:       newCheckRunner(cfg, store, notifier, selfAddr),
		term:         newTermManager(),
		desk:         newDeskManager(),
		forward:      newForwardManager(cfg),
		emailMgr:     newEmailManager(),
		playbooks:    newPlaybookManager(cfg),
		inspect:      newHostInspectManager(),
		push:         newPushHub(),
		agentUpdates: newAgentUpdateManager(),
		cicdWatcher:  newCICDFailureWatcher(),
		incidents:    newIncidentManager(),
		remediation:  newRemediationManager(cfg),
		slos:         newSLOManager(cfg),
		distProbes:   newDistProbeManager(),
		tickets:      newTicketManager(),
		oncall:       newOnCallManager(),
		changes:      newChangeManager(),
		logs:         newLogStore(),
		hw:           newHardwareStore(),
		hv:           newHypervStore(),
		snmp:         newSNMPStore(),
		nf:           newNFStore(),
		ai:           newAIManager(cfg),
		vm:           newVMWriter(cfg),
		messages:     newMessageHub(),
		aiStats:      newAIStatsHub(),
		assistStore:  newAssistStore(),
		aiGov:        newAIGovHub(),
		sqlChanges:   newSQLChangeRequestManager(),
	}
	s.checks.vm = s.vm                                            // 拨测结果持久化到 VM（重启后仍可查历史趋势）
	s.apimon = newAPIRunner(s.checks, cfg, store, notifier, s.vm) // API 性能监控（复用高级探测引擎）
	s.scrapes = newScrapeManager(cfg, s.vm)                       // 指标抓取：agentless 抓 exporter → VM
	s.promrules = newPromRuleManager(cfg, s.vm, notifier, store)  // 指标告警规则：PromQL → pushChannels → incident/AI
	s.wireSRE()
	// Restore persisted TCP forward rules (recreate listeners)
	s.forward.restoreRules(s)
	// Sreyun Agent 引擎是统一「AI 对话」的后端：无条件初始化（仅注册工具定义，很轻）。
	// 能否真正对话由请求时的 AI 配置（cfg.Enabled）决定——见 handleSreyunChat，
	// 未启用时优雅返回提示而非 503。此前 gated on SreyunEnabled&&Enabled 且仅在启动时
	// 判断，导致"配置完模型点 AI 对话仍 503"（s.sreyun 为 nil）。
	s.mcpClients = newMCPClientManager()
	_ = s.mcpClients.Reload(cfg.AIConfig().MCPClientsJSON)
	s.sreyun = newSreyunCore(s)
	secDir := cfg.securityDataDir()
	s.hostSec = newHostSecurityManager(secDir)
	cfg.migrateWebSecurityDefaultsOnce()
	cfg.migrateMySQLSlowSQLDefaultsOnce()
	s.webSec = newWebScanManager(secDir, cfg.WebSecurity().ScanConcurrency)
	s.feeds = newFeedManager(filepath.Join(secDir, "feeds"))
	s.feeds.onUpdated = s.reloadSQLErrorSignatures
	s.reloadSQLErrorSignatures()
	s.sqlHistory = newSQLQueryHistoryManager(secDir)
	s.sqlSlow = newSlowSQLManager(filepath.Join(secDir, "sql_slow"))
	s.secFindings = newSecurityFindingManager(secDir)
	if s.aiGov != nil {
		s.aiGov.load(filepath.Join(secDir, "ai_tool_audit.json"))
		s.aiGov.onRecord = func(e aiToolAuditEntry) {
			s.exportAIToolAuditEntry(e)
		}
	}
	s.startHostSecurityScheduler()
	s.startWebSecurityScheduler()
	s.startSecurityFeedScheduler()
	s.startSlowSQLScheduler()
	// AI 记忆异步写入 worker pool：3 个 worker，并发上限 3
	s.memoryCh = make(chan memoryJob, 100)
	s.memorySem = make(chan struct{}, 3)
	s.startMemoryWorkers()
	// 记忆生命周期管理定时任务：每天执行衰减 + 清理 + 容量裁剪
	if s.pg != nil {
		go func() {
			// 启动后立即执行一次
			s.pg.decayOldMemories()
			s.pg.cleanupExpiredMemories()
			s.pg.capMemoriesByKind(2000) // 每种 kind 最多 2000 条
			if n := s.pg.archiveStaleSkills(); n > 0 {
				slog.Info("已归档低质/过时技能", "count", n)
			}
			s.pg.cleanupFlowRecords() // 清理过期 Flow 记录
			s.pg.cleanupContentAudit(s.cfg.Retention().ContentAuditDays)
			s.pg.cleanupAuditAndEvents(s.cfg.Retention().AuditDays)
			s.pg.cleanupAlertHistory(s.cfg.Retention().AlertHistoryDays)
			s.pg.cleanupOldFlowPartitions(s.cfg.Retention().NetFlowMonths)
			s.pg.cleanupAICallEvents(s.cfg.Retention().AICallDays)
			s.startBackupScheduler()
			s.startTicketSLAWatcher()
			s.startSecretRotateScheduler()
			// 每 24 小时执行一次
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				s.pg.decayOldMemories()
				s.pg.cleanupExpiredMemories()
				s.pg.capMemoriesByKind(2000)
				if n := s.pg.archiveStaleSkills(); n > 0 {
					slog.Info("已归档低质/过时技能", "count", n)
				}
				s.pg.cleanupFlowRecords()
				ret := s.cfg.Retention()
				s.pg.cleanupContentAudit(ret.ContentAuditDays)
				s.pg.cleanupAuditAndEvents(ret.AuditDays)
				s.pg.cleanupAlertHistory(ret.AlertHistoryDays)
				s.pg.cleanupOldFlowPartitions(ret.NetFlowMonths)
				s.pg.cleanupAICallEvents(ret.AICallDays)
				// 自进化：默认仍提炼技能；开启 SelfEvolveEnabled 时额外写成长日记并做完整维护。
				if s.cfg.AIConfig().SelfEvolveEnabled {
					s.maybeRunScheduledSelfEvolve()
				} else {
					_, _ = s.distillSkills(14)
				}
			}
		}()
	}
	return s
}

// Routes builds the HTTP handler using Go 1.22 method+path patterns.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/agent/register", s.handleRegister)
	mux.HandleFunc("POST /api/v1/agent/report", s.handleReport)
	// terminal auth: secondary password + protocol agreement
	mux.HandleFunc("GET /api/user/terminal-password/status", s.handleTerminalPasswordStatus)
	mux.HandleFunc("POST /api/user/terminal-password/set", s.handleTerminalPasswordSet)
	mux.HandleFunc("POST /api/user/terminal-password/verify", s.handleTerminalPasswordVerify)
	// remote terminal: browser WebSocket (auth) + agent reverse streams (token)
	mux.HandleFunc("GET /api/v1/hosts/{id}/terminal", s.handleTerminal)
	mux.HandleFunc("GET /api/v1/hosts/{id}/remote-preflight", s.handleRemotePreflight)
	mux.HandleFunc("GET /api/v1/agent/terminal/wait", s.handleAgentTermWait)
	mux.HandleFunc("GET /api/v1/agent/terminal/rx", s.handleAgentTermRx)
	mux.HandleFunc("GET /api/v1/agent/terminal/alive", s.handleAgentTermAlive)
	mux.HandleFunc("POST /api/v1/agent/terminal/tx", s.handleAgentTermTx)
	mux.HandleFunc("GET /api/v1/hosts", s.handleHosts)
	mux.HandleFunc("GET /api/v1/agent-dist/manifest", s.handleAgentDistManifest)
	mux.HandleFunc("POST /api/v1/agents/update", s.handleAgentUpdateStart)
	mux.HandleFunc("GET /api/v1/agents/update/jobs", s.handleAgentUpdateJobs)
	mux.HandleFunc("GET /api/v1/agents/update/jobs/{id}", s.handleAgentUpdateJob)
	mux.HandleFunc("GET /api/v1/agents/auto-update-policy", s.handleAgentAutoUpdatePolicyGet)
	mux.HandleFunc("POST /api/v1/agents/auto-update-policy", s.handleAgentAutoUpdatePolicySet)
	mux.HandleFunc("GET /api/v1/agents/auto-update-status", s.handleAgentAutoUpdateStatusGet)
	mux.HandleFunc("GET /api/v1/resources/search", s.handleResourceSearch)
	mux.HandleFunc("GET /api/v1/hosts/{id}/metrics", s.handleHostMetrics)
	mux.HandleFunc("GET /api/v1/hosts/{id}/history", s.handleHostHistory)
	mux.HandleFunc("POST /api/v1/hosts/{id}/category", s.handleSetCategory)
	mux.HandleFunc("POST /api/v1/hosts/{id}/folder", s.handleSetHostFolder)
	mux.HandleFunc("POST /api/v1/hosts/{id}/desktop", s.handleOpenDesktop)
	mux.HandleFunc("GET /api/v1/hosts/{id}/desktop/ws", s.handleDesktopWS)
	mux.HandleFunc("GET /api/v1/agent/desktop/wait", s.handleAgentDeskWait)
	mux.HandleFunc("GET /api/v1/agent/desktop/rx", s.handleAgentDeskRx)
	mux.HandleFunc("POST /api/v1/agent/desktop/tx", s.handleAgentDeskTx)
	mux.HandleFunc("GET /api/v1/desktop/sessions", s.handleListDesktopSessions)
	mux.HandleFunc("GET /api/v1/desktop/sessions/{id}/replay", s.handleDesktopReplay)
	mux.HandleFunc("GET /api/v1/host-folders", s.handleGetHostFolders)
	mux.HandleFunc("PUT /api/v1/host-folders", s.handlePutHostFolders)
	mux.HandleFunc("POST /api/v1/host-folders", s.handlePostHostFolder)
	mux.HandleFunc("PATCH /api/v1/host-folders/{id}", s.handlePatchHostFolder)
	mux.HandleFunc("DELETE /api/v1/host-folders/{id}", s.handleDeleteHostFolder)
	mux.HandleFunc("DELETE /api/v1/hosts/{id}", s.handleDeleteHost)
	// 重复主机（Agent 重装导致同一台机器出现多条记录）识别与清理
	mux.HandleFunc("GET /api/v1/hosts/duplicates", s.handleHostDuplicates)
	mux.HandleFunc("POST /api/v1/hosts/duplicates/cleanup", s.handleCleanupDuplicates)
	mux.HandleFunc("GET /api/v1/alerts", s.handleAlerts)
	mux.HandleFunc("GET /api/v1/alerts/history", s.handleAlertHistory)
	mux.HandleFunc("POST /api/v1/alerts/ack", s.handleAlertAck)
	mux.HandleFunc("POST /api/v1/alerts/silence", s.handleAlertSilence)
	mux.HandleFunc("GET /api/v1/alerts/governance", s.handleGetGovernance)
	mux.HandleFunc("POST /api/v1/alerts/governance", s.handleSetGovernance)
	mux.HandleFunc("POST /api/v1/alerts/clear", s.handleAlertClear)
	mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	mux.HandleFunc("GET /api/v1/activity", s.handleActivity)
	mux.HandleFunc("GET /api/v1/summary", s.handleSummary)
	mux.HandleFunc("GET /api/v1/weather", s.handleWeather)
	mux.HandleFunc("GET /api/v1/config", s.handleGetConfig)
	mux.HandleFunc("POST /api/v1/config", s.handleSetConfig)
	mux.HandleFunc("POST /api/v1/config/test", s.handleTestConfig)
	mux.HandleFunc("GET /api/v1/config/threshold-presets", s.handleThresholdPresets)
	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/login/sms-code", s.handleLoginSMSCode)
	mux.HandleFunc("POST /api/v1/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/me", s.handleMe)
	mux.HandleFunc("POST /api/v1/profile", s.handleSetProfile)
	mux.HandleFunc("POST /api/v1/password", s.handleSetPassword)
	mux.HandleFunc("POST /api/v1/account/init", s.handleAccountInit)
	mux.HandleFunc("POST /api/v1/mfa/setup", s.handleMFASetup)
	mux.HandleFunc("POST /api/v1/mfa/enable", s.handleMFAEnable)
	mux.HandleFunc("POST /api/v1/mfa/disable", s.handleMFADisable)
	mux.HandleFunc("POST /api/v1/mfa/unbind-via-email", s.handleMFAUnbindViaEmail)
	mux.HandleFunc("GET /api/v1/mfa/global", s.handleMFAGlobalGet)
	mux.HandleFunc("POST /api/v1/mfa/global", s.handleMFAGlobalSet)
	// Account recovery: public endpoints (no session required)
	// New dual-verification flow (email code + optional MFA TOTP)
	mux.HandleFunc("POST /api/v1/account/recover-send-code", s.handleRecoverSendCode)
	mux.HandleFunc("POST /api/v1/account/recover-verify", s.handleRecoverVerify)
	mux.HandleFunc("POST /api/v1/account/recover-verify-mfa", s.handleRecoverVerifyMFA)
	// Legacy/backward-compat endpoints
	mux.HandleFunc("POST /api/v1/account/recover-username", s.handleRecoverUsername)
	mux.HandleFunc("POST /api/v1/account/send-reset-code", s.handleSendResetCode)
	mux.HandleFunc("POST /api/v1/account/reset-password", s.handleResetPassword)
	// user management (RBAC; admin-only, enforced by routeAllowed)
	mux.HandleFunc("GET /api/v1/users", s.handleListUsers)
	mux.HandleFunc("POST /api/v1/users", s.handleCreateUser)
	mux.HandleFunc("POST /api/v1/users/{username}", s.handleUpdateUser)
	mux.HandleFunc("DELETE /api/v1/users/{username}", s.handleDeleteUser)
	mux.HandleFunc("POST /api/v1/users/{username}/reset-password", s.handleResetUserPassword)
	mux.HandleFunc("POST /api/v1/users/{username}/reset-mfa", s.handleResetUserMFA)
	// 用户目录（viewer+）：工单指派等场景，不含敏感字段
	mux.HandleFunc("GET /api/v1/directory/users", s.handleDirectoryUsers)
	mux.HandleFunc("GET /api/v1/checks", s.handleGetChecks)
	mux.HandleFunc("POST /api/v1/checks", s.handleUpsertCheck)
	mux.HandleFunc("POST /api/v1/checks/{id}/run", s.handleRunCheck)
	mux.HandleFunc("GET /api/v1/checks/{id}/history", s.handleCheckHistory)
	mux.HandleFunc("DELETE /api/v1/checks/{id}", s.handleDeleteCheck)
	// API 性能监控：业务系统 + 接口批量监控
	mux.HandleFunc("GET /api/v1/apimon/systems", s.handleAPIMonOverview)
	mux.HandleFunc("POST /api/v1/apimon/systems", s.handleUpsertAPISystem)
	mux.HandleFunc("POST /api/v1/apimon/systems/{id}/run", s.handleRunAPISystem)
	mux.HandleFunc("DELETE /api/v1/apimon/systems/{id}", s.handleDeleteAPISystem)
	mux.HandleFunc("POST /api/v1/apimon/systems/{id}/maintenance", s.handleAPISystemMaint)
	mux.HandleFunc("GET /api/v1/apimon/endpoints/{id}/history", s.handleAPIEndpointHistory)
	mux.HandleFunc("GET /api/v1/apimon/transactions", s.handleAPITxnOverview)
	mux.HandleFunc("POST /api/v1/apimon/transactions", s.handleUpsertAPITransaction)
	mux.HandleFunc("DELETE /api/v1/apimon/transactions/{id}", s.handleDeleteAPITransaction)
	mux.HandleFunc("POST /api/v1/apimon/transactions/{id}/run", s.handleRunAPITransaction)
	mux.HandleFunc("GET /api/v1/apimon/distributed", s.handleDistStatus)
	mux.HandleFunc("POST /api/v1/apimon/import-openapi", s.handleImportOpenAPI)
	mux.HandleFunc("POST /api/v1/apimon/fetch-openapi", s.handleFetchOpenAPI)
	mux.HandleFunc("GET /api/v1/apimon/sla", s.handleSLAReport)
	mux.HandleFunc("GET /api/v1/scrape-targets", s.handleScrapeTargets)
	mux.HandleFunc("POST /api/v1/scrape-targets", s.handleUpsertScrapeTarget)
	mux.HandleFunc("DELETE /api/v1/scrape-targets/{id}", s.handleDeleteScrapeTarget)
	mux.HandleFunc("POST /api/v1/scrape-targets/{id}/run", s.handleRunScrapeTarget)
	mux.HandleFunc("GET /api/v1/prom/write-config", s.handleGetPromWrite)
	mux.HandleFunc("POST /api/v1/prom/write-config", s.handleSetPromWriteToken)
	mux.HandleFunc("POST /api/v1/prom/write", s.handlePromRemoteWrite)
	mux.HandleFunc("GET /api/v1/prom-rules", s.handleListPromRules)
	mux.HandleFunc("POST /api/v1/prom-rules", s.handleUpsertPromRule)
	mux.HandleFunc("DELETE /api/v1/prom-rules/{id}", s.handleDeletePromRule)
	mux.HandleFunc("POST /api/v1/prom-rules/test", s.handleTestPromRule)
	// 仪表盘：自定义 + 导入 Grafana，面板查询走 VM
	mux.HandleFunc("GET /api/v1/dashboards", s.handleListDashboards)
	mux.HandleFunc("POST /api/v1/dashboards", s.handleUpsertDashboard)
	mux.HandleFunc("GET /api/v1/dashboards/assets/{dashID}/{name}", s.handleGetDashboardAsset)
	mux.HandleFunc("GET /api/v1/dashboards/{id}", s.handleGetDashboard)
	mux.HandleFunc("DELETE /api/v1/dashboards/{id}", s.handleDeleteDashboard)
	mux.HandleFunc("POST /api/v1/dashboards/{id}/assets", s.handleUploadDashboardAsset)
	mux.HandleFunc("POST /api/v1/dashboards/query", s.handleDashboardQuery)
	mux.HandleFunc("POST /api/v1/dashboards/query-forecast", s.handleDashboardQueryForecast)
	mux.HandleFunc("POST /api/v1/metrics/forecast", s.handleMetricsForecast)
	mux.HandleFunc("POST /api/v1/dashboards/query-instant", s.handleDashboardQueryInstant)
	mux.HandleFunc("POST /api/v1/dashboards/query-logs", s.handleDashboardQueryLogs)
	mux.HandleFunc("POST /api/v1/dashboards/query-sql", s.handleDashboardQuerySQL)
	mux.HandleFunc("POST /api/v1/dashboards/var-values", s.handleDashboardVarValues)
	mux.HandleFunc("POST /api/v1/dashboards/import-grafana", s.handleImportGrafana)
	// 仪表盘 AI 闭环：自然语言生成 / 按事件生成分析看板 / 实时摘要 / 研判转工单
	mux.HandleFunc("POST /api/v1/dashboards/ai-create", s.handleAICreateDashboard)
	// 使用 ai/jobs（多一段字面量），避免与 {id}/digest 在 ServeMux 下交叉冲突
	mux.HandleFunc("GET /api/v1/dashboards/ai/jobs/{id}", s.handleGetDashboardAIJob)
	mux.HandleFunc("POST /api/v1/dashboards/ai-from-incident", s.handleAIDashboardFromIncident)
	mux.HandleFunc("GET /api/v1/dashboards/{id}/digest", s.handleDashboardDigest)
	mux.HandleFunc("POST /api/v1/dashboards/{id}/ai-ticket", s.handleDashboardAITicket)
	mux.HandleFunc("POST /api/v1/dashboards/{id}/ai-apply", s.handleApplyDashOptimize)
	mux.HandleFunc("GET /api/v1/apimon/systems/{id}/hosts", s.handleAPISystemHosts)
	mux.HandleFunc("POST /api/v1/agent/probe-results", s.handleProbeResults)
	// Playbooks (automation)
	mux.HandleFunc("GET /api/v1/playbooks", s.handleListPlaybooks)
	mux.HandleFunc("POST /api/v1/playbooks", s.handleUpsertPlaybook)
	mux.HandleFunc("GET /api/v1/playbooks/packs", s.handleListPlaybookPacks)
	mux.HandleFunc("POST /api/v1/playbooks/packs/import", s.handleImportPlaybookPacks)
	mux.HandleFunc("DELETE /api/v1/playbooks/{id}", s.handleDeletePlaybook)
	mux.HandleFunc("GET /api/v1/playbooks/{id}/revisions", s.handleListPlaybookRevisions)
	mux.HandleFunc("GET /api/v1/playbooks/{id}/revisions/diff", s.handleDiffPlaybookRevisions)
	mux.HandleFunc("POST /api/v1/playbooks/{id}/revisions/{rev}/restore", s.handleRestorePlaybookRevision)
	mux.HandleFunc("GET /api/v1/playbooks/{id}/preflight", s.handlePlaybookPreflight)
	mux.HandleFunc("POST /api/v1/playbooks/{id}/execute", s.handleExecutePlaybook)
	mux.HandleFunc("GET /api/v1/playbooks/executions", s.handleListExecutions)
	// 使用 executions/by-id（多一段字面量），避免与 {id}/preflight 在 ServeMux 下交叉冲突
	mux.HandleFunc("GET /api/v1/playbooks/executions/by-id/{id}", s.handleGetExecution)
	mux.HandleFunc("POST /api/v1/playbooks/executions/by-id/{id}/approve", s.handleApprovePlaybookExecution)
	mux.HandleFunc("POST /api/v1/playbooks/executions/by-id/{id}/reject", s.handleRejectPlaybookExecution)
	mux.HandleFunc("POST /api/v1/playbooks/executions/by-id/{id}/cancel", s.handleCancelPlaybookExecution)
	// Host deep inspect (linux_inspect-style, agent module host_inspect)
	mux.HandleFunc("GET /api/v1/host-inspect", s.handleListHostInspect)
	mux.HandleFunc("GET /api/v1/host-inspect/compare", s.handleCompareHostInspect)
	mux.HandleFunc("GET /api/v1/host-inspect/{id}", s.handleGetHostInspect)
	mux.HandleFunc("POST /api/v1/host-inspect/run", s.handleRunHostInspect)
	// SRE workflow: incidents / auto-remediation / SLOs / work orders
	mux.HandleFunc("GET /api/v1/sre/overview", s.handleSREOverview)
	mux.HandleFunc("GET /api/v1/incidents", s.handleListIncidents)
	mux.HandleFunc("POST /api/v1/incidents", s.handleCreateIncident)
	mux.HandleFunc("GET /api/v1/incidents/{id}", s.handleGetIncident)
	mux.HandleFunc("POST /api/v1/incidents/{id}/ack", s.handleAckIncident)
	mux.HandleFunc("POST /api/v1/incidents/{id}/resolve", s.handleResolveIncident)
	mux.HandleFunc("POST /api/v1/incidents/{id}/comment", s.handleCommentIncident)
	mux.HandleFunc("POST /api/v1/incidents/{id}/ticket", s.handleEscalateIncident)
	mux.HandleFunc("GET /api/v1/remediation/rules", s.handleListRemediationRules)
	mux.HandleFunc("POST /api/v1/remediation/rules", s.handleUpsertRemediationRule)
	mux.HandleFunc("DELETE /api/v1/remediation/rules/{id}", s.handleDeleteRemediationRule)
	mux.HandleFunc("GET /api/v1/remediation/runs", s.handleListRemediationRuns)
	mux.HandleFunc("POST /api/v1/remediation/runs/{id}/approve", s.handleApproveRemediation)
	mux.HandleFunc("POST /api/v1/remediation/runs/{id}/reject", s.handleRejectRemediation)
	mux.HandleFunc("POST /api/v1/incidents/{id}/remediation-propose", s.handleProposeRemediation) // L4：事件剧本草稿→待审批
	mux.HandleFunc("GET /api/v1/incidents/{id}/loop", s.handleGetIncidentLoop)
	mux.HandleFunc("POST /api/v1/incidents/{id}/loop/{action}", s.handleIncidentLoopAction)
	mux.HandleFunc("GET /api/v1/incidents/{id}/case-export", s.handleIncidentCaseExport)
	mux.HandleFunc("GET /api/v1/sre/effect", s.handleSREEffect)
	mux.HandleFunc("GET /api/v1/services", s.handleListBusinessServices)
	mux.HandleFunc("POST /api/v1/services", s.handleUpsertBusinessService)
	mux.HandleFunc("DELETE /api/v1/services/{id}", s.handleDeleteBusinessService)
	mux.HandleFunc("GET /api/v1/services/{id}/impact", s.handleBusinessServiceImpact)
	mux.HandleFunc("GET /api/v1/changes/{id}/impact", s.handleChangeImpact)
	mux.HandleFunc("GET /api/v1/changes/{id}/blast-radius", s.handleChangeImpact) // alias: 爆炸半径预估
	mux.HandleFunc("GET /api/v1/topology/edges", s.handleListTopologyEdges)
	mux.HandleFunc("POST /api/v1/topology/edges", s.handleUpsertTopologyEdge)
	mux.HandleFunc("DELETE /api/v1/topology/edges/{id}", s.handleDeleteTopologyEdge)
	mux.HandleFunc("GET /api/v1/topology/auto-discover", s.handleDiscoverAutoTopology)
	mux.HandleFunc("POST /api/v1/topology/auto-discover", s.handleDiscoverAutoTopology)
	mux.HandleFunc("GET /api/v1/topology/rca", s.handleTopologyRCA)
	mux.HandleFunc("GET /api/v1/slos", s.handleListSLOs)
	mux.HandleFunc("POST /api/v1/slos", s.handleUpsertSLO)
	mux.HandleFunc("DELETE /api/v1/slos/{id}", s.handleDeleteSLO)
	mux.HandleFunc("GET /api/v1/slos/{id}/trend", s.handleSLOTrend)
	mux.HandleFunc("GET /api/v1/tickets", s.handleListTickets)
	mux.HandleFunc("POST /api/v1/tickets", s.handleCreateTicket)
	mux.HandleFunc("GET /api/v1/tickets/sla", s.handleGetTicketSLA)
	mux.HandleFunc("POST /api/v1/tickets/sla", s.handleSetTicketSLA)
	mux.HandleFunc("GET /api/v1/tickets/sla/breaches", s.handleTicketSLABreaches)
	mux.HandleFunc("GET /api/v1/tickets/{id}", s.handleGetTicket)
	mux.HandleFunc("POST /api/v1/tickets/{id}/link", s.handleTicketLink)
	mux.HandleFunc("GET /api/v1/service-request/catalog", s.handleServiceRequestCatalog)
	mux.HandleFunc("POST /api/v1/incidents/{id}/emergency-change", s.handleIncidentEmergencyChange)
	mux.HandleFunc("POST /api/v1/incidents/{id}/link-ticket", s.handleIncidentLinkTicket)
	// On-call
	mux.HandleFunc("GET /api/v1/oncall/who", s.handleOnCallWho)
	mux.HandleFunc("GET /api/v1/oncall/schedules", s.handleListOnCallSchedules)
	mux.HandleFunc("POST /api/v1/oncall/schedules", s.handleUpsertOnCallSchedule)
	mux.HandleFunc("DELETE /api/v1/oncall/schedules/{id}", s.handleDeleteOnCallSchedule)
	mux.HandleFunc("GET /api/v1/oncall/policies", s.handleListEscalationPolicies)
	mux.HandleFunc("POST /api/v1/oncall/policies", s.handleUpsertEscalationPolicy)
	mux.HandleFunc("DELETE /api/v1/oncall/policies/{id}", s.handleDeleteEscalationPolicy)
	mux.HandleFunc("GET /api/v1/oncall/pages", s.handleListOnCallPages)
	// Changes
	mux.HandleFunc("GET /api/v1/changes/windows", s.handleListChangeWindows)
	mux.HandleFunc("POST /api/v1/changes/windows", s.handleUpsertChangeWindow)
	mux.HandleFunc("DELETE /api/v1/changes/windows/{id}", s.handleDeleteChangeWindow)
	mux.HandleFunc("GET /api/v1/changes", s.handleListChanges)
	mux.HandleFunc("POST /api/v1/changes", s.handleUpsertChange)
	mux.HandleFunc("GET /api/v1/changes/{id}", s.handleGetChange)
	mux.HandleFunc("POST /api/v1/changes/{id}/link-incident", s.handleLinkChangeIncident)
	mux.HandleFunc("POST /api/v1/changes/{id}/link", s.handleChangeLink)
	mux.HandleFunc("POST /api/v1/changes/{id}/submit", s.handleChangeSubmit)
	mux.HandleFunc("POST /api/v1/changes/{id}/approve", s.handleChangeApprove)
	mux.HandleFunc("POST /api/v1/changes/{id}/reject", s.handleChangeReject)
	mux.HandleFunc("POST /api/v1/changes/{id}/start", s.handleChangeStart)
	mux.HandleFunc("POST /api/v1/changes/{id}/complete", s.handleChangeComplete)
	mux.HandleFunc("POST /api/v1/changes/{id}/rollback", s.handleChangeRollback)
	mux.HandleFunc("POST /api/v1/changes/{id}/cancel", s.handleChangeCancel)
	mux.HandleFunc("POST /api/v1/changes/{id}/schedule", s.handleChangeSchedule)
	mux.HandleFunc("GET /api/v1/incidents/{id}/related-changes", s.handleIncidentRelatedChanges)
	mux.HandleFunc("POST /api/v1/incidents/{id}/assign", s.handleAssignIncident)
	// Admin: backup / retention / cmd policy
	mux.HandleFunc("GET /api/v1/admin/backups", s.handleListBackups)
	mux.HandleFunc("POST /api/v1/admin/backups", s.handleCreateBackup)
	mux.HandleFunc("GET /api/v1/admin/backups/{id}/download", s.handleDownloadBackup)
	mux.HandleFunc("POST /api/v1/admin/backups/{id}/restore", s.handleRestoreBackup)
	mux.HandleFunc("GET /api/v1/admin/retention", s.handleGetRetention)
	mux.HandleFunc("POST /api/v1/admin/retention", s.handleSetRetention)
	mux.HandleFunc("GET /api/v1/admin/backup-config", s.handleGetBackupCfg)
	mux.HandleFunc("POST /api/v1/admin/backup-config", s.handleSetBackupCfg)
	mux.HandleFunc("GET /api/v1/admin/cmd-policy", s.handleGetCmdPolicy)
	mux.HandleFunc("POST /api/v1/admin/cmd-policy", s.handleSetCmdPolicy)
	mux.HandleFunc("GET /api/v1/admin/ai/billing-reconcile", s.handleAIBillingReconcile)
	mux.HandleFunc("GET /api/v1/admin/ai/prompts", s.handleAIPrompts)
	mux.HandleFunc("POST /api/v1/tickets/{id}", s.handleUpdateTicket)
	mux.HandleFunc("POST /api/v1/tickets/{id}/comment", s.handleCommentTicket)
	mux.HandleFunc("DELETE /api/v1/tickets/{id}", s.handleDeleteTicket)
	// Log aggregation (agent ingest is fingerprint-authed) + search + diagnosis
	mux.HandleFunc("POST /api/v1/agent/logs", s.handleAgentLogs)
	mux.HandleFunc("GET /api/v1/logs", s.handleSearchLogs)
	mux.HandleFunc("POST /api/v1/logs/diagnose", s.handleLogDiagnose)
	// Notification center (unified message feed)
	mux.HandleFunc("GET /api/v1/messages", s.handleListMessages)
	mux.HandleFunc("POST /api/v1/messages/read", s.handleMarkMessagesRead)
	mux.HandleFunc("POST /api/v1/messages/read-all", s.handleMarkAllMessagesRead)
	// AI: config + inspection + incident diagnosis
	mux.HandleFunc("GET /api/v1/ai/config", s.handleGetAIConfig)
	mux.HandleFunc("POST /api/v1/ai/config", s.handleSetAIConfig)
	mux.HandleFunc("GET /api/v1/ai/tool-audit", s.handleListAIToolAudit)
	mux.HandleFunc("GET /api/v1/audit-export", s.handleGetAuditExport)
	mux.HandleFunc("POST /api/v1/audit-export", s.handleSetAuditExport)
	mux.HandleFunc("GET /api/v1/auth/oidc/config", s.handleGetOIDCConfig)
	mux.HandleFunc("POST /api/v1/auth/oidc/config", s.handleSetOIDCConfig)
	mux.HandleFunc("GET /api/v1/auth/oidc/info", s.handleOIDCLoginInfo)
	mux.HandleFunc("GET /api/v1/auth/oidc/login", s.handleOIDCLogin)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", s.handleOIDCCallback)
	mux.HandleFunc("GET /api/v1/auth/sso/config", s.handleGetSSOConfig)
	mux.HandleFunc("POST /api/v1/auth/sso/config", s.handleSetSSOConfig)
	mux.HandleFunc("GET /api/v1/auth/sso/info", s.handleSSOLoginInfo)
	mux.HandleFunc("GET /api/v1/auth/feishu/login", s.handleSSOLogin)
	mux.HandleFunc("GET /api/v1/auth/feishu/callback", s.handleSSOCallback)
	mux.HandleFunc("GET /api/v1/auth/dingtalk/login", s.handleSSOLogin)
	mux.HandleFunc("GET /api/v1/auth/dingtalk/callback", s.handleSSOCallback)
	mux.HandleFunc("GET /api/v1/auth/wechat/login", s.handleSSOLogin)
	mux.HandleFunc("GET /api/v1/auth/wechat/callback", s.handleSSOCallback)
	mux.HandleFunc("GET /api/v1/auth/wecom/login", s.handleSSOLogin)
	mux.HandleFunc("GET /api/v1/auth/wecom/callback", s.handleSSOCallback)
	mux.HandleFunc("GET /api/v1/auth/sso/identities", s.handleListSSOIdentities)
	mux.HandleFunc("DELETE /api/v1/auth/sso/identities/{provider}", s.handleUnbindSSOIdentity)
	mux.HandleFunc("POST /api/v1/ai/test", s.handleTestAIConfig)
	mux.HandleFunc("POST /api/v1/ai/test-embed", s.handleTestEmbedConfig)
	mux.HandleFunc("POST /api/v1/ai/test-speech", s.handleTestSpeechConfig)
	mux.HandleFunc("POST /api/v1/ai/speech/stt", s.handleAISpeechSTT)
	mux.HandleFunc("POST /api/v1/ai/speech/tts", s.handleAISpeechTTS)
	mux.HandleFunc("GET /api/v1/ai/speech/status", s.handleAISpeechStatus)
	mux.HandleFunc("POST /api/v1/ai/test-rerank", s.handleTestRerankConfig)
	mux.HandleFunc("POST /api/v1/ai/test-weknora", s.handleTestWeKnoraConfig)
	mux.HandleFunc("POST /api/v1/ai/list-weknora-kbs", s.handleListWeKnoraKBs)
	mux.HandleFunc("POST /api/v1/ai/mcp-clients/test", s.handleTestMCPClient)
	mux.HandleFunc("POST /api/v1/ai/mcp-clients/sync", s.handleSyncMCPClient)
	mux.HandleFunc("POST /api/v1/ai/terminal-access", s.handleAITerminalAccess)
	mux.HandleFunc("POST /api/v1/ai/chat", s.handleAIChat)
	mux.HandleFunc("POST /api/v1/ai/assist", s.handleAIAssist)                     // 全站「AI 辅助」按钮统一入口（任务化 SSE）
	mux.HandleFunc("POST /api/v1/ai/assist/feedback", s.handleAIAssistFeedback)    // 采纳/评价 AI 辅助结果 → 学习闭环强化
	mux.HandleFunc("POST /api/v1/ai/write-approval", s.handleIssueAIWriteApproval) // 写工具 per-action 审批令牌
	mux.HandleFunc("GET /api/v1/ai/runs", s.handleListAIRuns)                      // Wave2 AI Run 列表
	mux.HandleFunc("GET /api/v1/ai/runs/{id}", s.handleGetAIRun)                   // Wave2 AI Run 详情
	mux.HandleFunc("GET /api/v1/ai/duty-context", s.handleDutyContext)             // 值班晨报态势汇总（供前端流式生成）
	mux.HandleFunc("GET /api/v1/ai/copilot/context", s.handleAICopilotContext)     // On-call Copilot 工作台上下文
	mux.HandleFunc("GET /api/v1/ai/skill-packs", s.handleListSkillPacks)           // 行业知识包清单
	mux.HandleFunc("POST /api/v1/ai/skill-packs/import", s.handleImportSkillPacks)
	mux.HandleFunc("GET /api/v1/ai/skills/export", s.handleExportCustomerSkillPack)
	mux.HandleFunc("POST /api/v1/ai/skills/import", s.handleImportCustomerSkillPack)
	mux.HandleFunc("GET /api/v1/ai/skills", s.handleListSkills) // AI 技能库（自进化提炼产物）
	mux.HandleFunc("DELETE /api/v1/ai/skills/{id}", s.handleDeleteSkill)
	mux.HandleFunc("POST /api/v1/ai/skills/{id}/archive", s.handleArchiveSkill)
	mux.HandleFunc("POST /api/v1/ai/skills/{id}/scope", s.handleSetSkillScope)
	mux.HandleFunc("POST /api/v1/ai/skills/merge", s.handleMergeSkills)
	mux.HandleFunc("POST /api/v1/ai/skills/distill", s.handleDistillSkills) // 手动触发技能提炼
	mux.HandleFunc("GET /api/v1/ai/memories", s.handleListMemories)         // AI 记忆浏览器（只读列表 + 可删）
	mux.HandleFunc("DELETE /api/v1/ai/memories/{id}", s.handleDeleteMemory)
	mux.HandleFunc("GET /api/v1/ai/stats", s.handleAIStats)                // AI 调用延迟/失败率/粗估 token 仪表（PG 永久）
	mux.HandleFunc("GET /api/v1/ai/usage/history", s.handleAIUsageHistory) // 成本/Token 历史组合曲线
	mux.HandleFunc("GET /api/v1/ai/usage/by-user", s.handleAIUsageByUser)  // 按用户成本分析
	mux.HandleFunc("GET /api/v1/ai/experiments/stats", s.handleAIExperimentStats)
	mux.HandleFunc("GET /api/v1/ai/experiments", s.handleListAIExperiments)
	mux.HandleFunc("POST /api/v1/ai/experiments", s.handleUpsertAIExperiment)
	mux.HandleFunc("PUT /api/v1/ai/experiments/{id}", s.handleUpsertAIExperiment)
	mux.HandleFunc("DELETE /api/v1/ai/experiments/{id}", s.handleDeleteAIExperiment)
	mux.HandleFunc("POST /api/v1/ops/actions/validate", s.handleOpsActionsValidate)
	mux.HandleFunc("POST /api/v1/ops/actions/apply", s.handleOpsActionsApply)
	mux.HandleFunc("GET /api/v1/audit/verify-chain", s.handleAuditVerifyChain)
	mux.HandleFunc("POST /api/v1/security/rewrap-secrets", s.handleSecurityRewrap)
	mux.HandleFunc("GET /api/v1/security/key-status", s.handleSecurityKeyStatus)
	mux.HandleFunc("GET /api/v1/security/secret-rotate", s.handleSecretRotateStatus)
	mux.HandleFunc("POST /api/v1/security/secret-rotate", s.handleRotateSecretKey)
	// Public Status Page + admin config
	mux.HandleFunc("GET /status", s.handlePublicStatusHTML)
	mux.HandleFunc("GET /api/v1/public/status", s.handlePublicStatusJSON)
	mux.HandleFunc("GET /api/v1/brand", s.handleGetBrand)
	mux.HandleFunc("GET /api/v1/brand/logo/{name}", s.handleGetBrandLogo)
	mux.HandleFunc("GET /api/v1/admin/brand", s.handleGetAdminBrand)
	mux.HandleFunc("POST /api/v1/admin/brand", s.handleSetAdminBrand)
	mux.HandleFunc("POST /api/v1/admin/brand/logo", s.handleUploadBrandLogo)
	mux.HandleFunc("DELETE /api/v1/admin/brand/logo", s.handleDeleteBrandLogo)
	mux.HandleFunc("GET /api/v1/admin/status-page", s.handleGetStatusPageConfig)
	mux.HandleFunc("POST /api/v1/admin/status-page", s.handleSetStatusPageConfig)
	mux.HandleFunc("GET /api/v1/admin/pg/slow-queries", s.handlePGSlowQueries)
	mux.HandleFunc("GET /api/v1/admin/netflow/queue", s.handleNetflowQueueStats)
	mux.HandleFunc("GET /api/v1/terminal/commands", s.handleTerminalCommands) // 终端命令永久历史（audit_log）
	mux.HandleFunc("GET /api/v1/mcp", s.handleMCP)                            // MCP Streamable HTTP：SSE 长连接 / 探测
	mux.HandleFunc("POST /api/v1/mcp", s.handleMCP)                           // MCP Streamable HTTP：JSON-RPC（JSON 或 SSE 响应）
	mux.HandleFunc("DELETE /api/v1/mcp", s.handleMCP)                         // MCP 会话结束（无服务端态，200）
	mux.HandleFunc("POST /api/v1/ai/models", s.handleAIModels)
	mux.HandleFunc("GET /api/v1/ai/inspections", s.handleListInspections)
	mux.HandleFunc("POST /api/v1/ai/inspect", s.handleRunInspection)
	mux.HandleFunc("POST /api/v1/incidents/{id}/diagnose", s.handleDiagnoseIncident)
	mux.HandleFunc("POST /api/v1/incidents/{id}/diagnose-chat", s.handleDiagnoseChatIncident)
	mux.HandleFunc("GET /api/v1/incidents/{id}/diagnose-chat", s.handleGetDiagnosisChatHistory)
	mux.HandleFunc("POST /api/v1/incidents/{id}/diagnosis-feedback", s.handleDiagnosisFeedback)
	// AI 经验规则库
	mux.HandleFunc("GET /api/v1/experience-rules", s.handleListExperienceRules)
	mux.HandleFunc("POST /api/v1/experience-rules", s.handleCreateExperienceRule)
	mux.HandleFunc("DELETE /api/v1/experience-rules/{id}", s.handleDeleteExperienceRule)
	// Sreyun Agent — 自主运维 Agent
	mux.HandleFunc("POST /api/v1/hermes/chat", s.handleSreyunChat)
	mux.HandleFunc("GET /api/v1/hermes/suggestions", s.handleSreyunSuggestions)
	mux.HandleFunc("GET /api/v1/hermes/status", s.handleSreyunStatus)
	mux.HandleFunc("POST /api/v1/hermes/parse", s.handleSreyunParse)
	mux.HandleFunc("GET /api/v1/hermes/sessions", s.handleSreyunSessions)
	mux.HandleFunc("GET /api/v1/hermes/sessions/{id}", s.handleSreyunSession)
	mux.HandleFunc("POST /api/v1/hermes/sessions/{id}/undo", s.handleSreyunSessionUndo)
	mux.HandleFunc("GET /api/v1/hermes/rules", s.handleSreyunListRules)
	mux.HandleFunc("POST /api/v1/hermes/rules", s.handleSreyunUpsertRule)
	mux.HandleFunc("DELETE /api/v1/hermes/rules/{id}", s.handleSreyunDeleteRule)
	mux.HandleFunc("GET /api/v1/hermes/templates", s.handleSreyunListTemplates)
	mux.HandleFunc("POST /api/v1/hermes/templates", s.handleSreyunUpsertTemplate)
	mux.HandleFunc("DELETE /api/v1/hermes/templates/{id}", s.handleSreyunDeleteTemplate)
	// Terminal enhancements
	mux.HandleFunc("GET /api/v1/terminal/sessions", s.handleListTerminalSessions)
	mux.HandleFunc("GET /api/v1/terminal/sessions/{id}/replay", s.handleTerminalReplay)
	mux.HandleFunc("GET /api/v1/terminal/sessions/{id}/observe", s.handleTerminalObserve)
	// Port forwarding (TCP mapping + HTTP reverse proxy)
	mux.HandleFunc("GET /api/v1/forward", s.handleForwardList)
	mux.HandleFunc("POST /api/v1/forward", s.handleForwardCreate)
	mux.HandleFunc("DELETE /api/v1/forward/{id}", s.handleForwardDelete)
	mux.HandleFunc("PUT /api/v1/forward/{id}", s.handleForwardEdit)
	mux.HandleFunc("PUT /api/v1/forward/{id}/toggle", s.handleForwardToggle)
	mux.HandleFunc("POST /api/v1/forward/{id}/copy", s.handleForwardCopy)
	// 端口范围批量组：整组删除 / 启停 / 复制 / 编辑（避免几百条逐条操作）
	mux.HandleFunc("DELETE /api/v1/forward/group/{gid}", s.handleForwardGroupDelete)
	mux.HandleFunc("PUT /api/v1/forward/group/{gid}/toggle", s.handleForwardGroupToggle)
	mux.HandleFunc("POST /api/v1/forward/group/{gid}/copy", s.handleForwardGroupCopy)
	mux.HandleFunc("PUT /api/v1/forward/group/{gid}/edit", s.handleForwardGroupEdit)
	mux.HandleFunc("GET /api/v1/forward/stats", s.handleForwardStats)
	mux.HandleFunc("GET /api/v1/forward/health", s.handleForwardHealth)
	// HTTP proxy shortcuts (saved configs)
	mux.HandleFunc("GET /api/v1/http-proxy", s.handleHTTPProxyList)
	mux.HandleFunc("POST /api/v1/http-proxy", s.handleHTTPProxyCreate)
	mux.HandleFunc("DELETE /api/v1/http-proxy/{id}", s.handleHTTPProxyDelete)
	mux.HandleFunc("PUT /api/v1/http-proxy/{id}", s.handleHTTPProxyEdit)
	mux.HandleFunc("PUT /api/v1/http-proxy/{id}/toggle", s.handleHTTPProxyToggle)
	mux.HandleFunc("POST /api/v1/http-proxy/{id}/copy", s.handleHTTPProxyCopy)
	// External data sources (Loki / Prometheus): AI query + log search + alert queries
	// ---- CI/CD (GitLab CI / GitHub Actions / Gitee Go) ----
	mux.HandleFunc("GET /api/v1/cicd/connections", s.handleCICDConnectionList)
	mux.HandleFunc("POST /api/v1/cicd/connections", s.handleCICDConnectionCreate)
	mux.HandleFunc("POST /api/v1/cicd/connections/test", s.handleCICDConnectionTest)
	mux.HandleFunc("PUT /api/v1/cicd/connections/{id}", s.handleCICDConnectionUpdate)
	mux.HandleFunc("DELETE /api/v1/cicd/connections/{id}", s.handleCICDConnectionDelete)
	mux.HandleFunc("GET /api/v1/cicd/overview", s.handleCICDOverview)
	mux.HandleFunc("GET /api/v1/cicd/runs", s.handleCICDRuns)
	mux.HandleFunc("GET /api/v1/cicd/runs/{id}/jobs", s.handleCICDRunJobs)
	mux.HandleFunc("GET /api/v1/cicd/jobs/{id}/log", s.handleCICDJobLog)
	mux.HandleFunc("POST /api/v1/cicd/runs/{id}/retry", s.handleCICDRunRetry)
	mux.HandleFunc("POST /api/v1/cicd/runs/{id}/cancel", s.handleCICDRunCancel)
	mux.HandleFunc("POST /api/v1/cicd/runs/{id}/diagnose", s.handleCICDRunDiagnose)
	mux.HandleFunc("POST /api/v1/cicd/trigger", s.handleCICDTrigger)

	mux.HandleFunc("GET /api/v1/datasources", s.handleDataSourceList)
	mux.HandleFunc("POST /api/v1/datasources", s.handleDataSourceCreate)
	mux.HandleFunc("POST /api/v1/datasources/test", s.handleDataSourceTest)
	mux.HandleFunc("PUT /api/v1/datasources/{id}", s.handleDataSourceUpdate)
	mux.HandleFunc("DELETE /api/v1/datasources/{id}", s.handleDataSourceDelete)
	mux.HandleFunc("POST /api/v1/datasources/{id}/query", s.handleDataSourceQuery)
	mux.HandleFunc("GET /api/v1/datasources/{id}/labels", s.handleDataSourceLabels)
	mux.HandleFunc("GET /api/v1/k8s/clusters", s.handleListK8sClusters)
	mux.HandleFunc("POST /api/v1/k8s/clusters", s.handleUpsertK8sCluster)
	mux.HandleFunc("GET /api/v1/k8s/clusters/{id}", s.handleGetK8sCluster)
	mux.HandleFunc("PUT /api/v1/k8s/clusters/{id}", s.handleUpsertK8sCluster)
	mux.HandleFunc("DELETE /api/v1/k8s/clusters/{id}", s.handleDeleteK8sCluster)
	mux.HandleFunc("POST /api/v1/k8s/clusters/{id}/test", s.handleTestK8sCluster)
	mux.HandleFunc("GET /api/v1/k8s/clusters/{id}/overview", s.handleK8sOverview)
	mux.HandleFunc("GET /api/v1/k8s/clusters/{id}/namespaces", s.handleK8sNamespaces)
	mux.HandleFunc("GET /api/v1/k8s/clusters/{id}/nodes", s.handleK8sNodes)
	mux.HandleFunc("GET /api/v1/k8s/clusters/{id}/pods", s.handleK8sPods)
	mux.HandleFunc("GET /api/v1/k8s/clusters/{id}/deployments", s.handleK8sDeployments)
	mux.HandleFunc("GET /api/v1/k8s/clusters/{id}/events", s.handleK8sEvents)
	mux.HandleFunc("GET /api/v1/k8s/clusters/{id}/pods/{ns}/{name}/log", s.handleK8sPodLog)
	mux.HandleFunc("POST /api/v1/k8s/clusters/{id}/deployments/{ns}/{name}/scale", s.handleK8sScaleDeployment)
	mux.HandleFunc("POST /api/v1/k8s/clusters/{id}/deployments/{ns}/{name}/restart", s.handleK8sRestartDeployment)
	mux.HandleFunc("POST /api/v1/k8s/clusters/{id}/deployments/{ns}/{name}/undo", s.handleK8sUndoDeployment)
	mux.HandleFunc("DELETE /api/v1/k8s/clusters/{id}/pods/{ns}/{name}", s.handleK8sDeletePod)
	mux.HandleFunc("POST /api/v1/k8s/clusters/{id}/pods/{ns}/{name}/exec", s.handleK8sPodExec)
	mux.HandleFunc("POST /api/v1/k8s/clusters/{id}/apply", s.handleK8sApply)
	mux.HandleFunc("POST /api/v1/k8s/clusters/{id}/namespaces", s.handleK8sCreateNamespace)
	// SQL toolkit (MySQL beautify / audit / optimize + read-only connections)
	mux.HandleFunc("POST /api/v1/sql/beautify", s.handleSQLBeautify)
	mux.HandleFunc("POST /api/v1/sql/audit", s.handleSQLAudit)
	mux.HandleFunc("POST /api/v1/sql/optimize", s.handleSQLOptimize)
	mux.HandleFunc("POST /api/v1/sql/analyze", s.handleSQLAnalyze)
	mux.HandleFunc("GET /api/v1/sql/connections", s.handleListMySQLConnections)
	mux.HandleFunc("POST /api/v1/sql/connections", s.handleUpsertMySQLConnection)
	mux.HandleFunc("GET /api/v1/sql/connections/{id}", s.handleGetMySQLConnection)
	mux.HandleFunc("PUT /api/v1/sql/connections/{id}", s.handleUpsertMySQLConnection)
	mux.HandleFunc("DELETE /api/v1/sql/connections/{id}", s.handleDeleteMySQLConnection)
	mux.HandleFunc("POST /api/v1/sql/connections/{id}/test", s.handleTestMySQLConnection)
	mux.HandleFunc("POST /api/v1/sql/connections/{id}/explain", s.handleMySQLExplain)
	mux.HandleFunc("POST /api/v1/sql/connections/{id}/query", s.handleSQLWorkbenchQuery)
	mux.HandleFunc("POST /api/v1/sql/connections/{id}/exec-ddl", s.handleMySQLExecDDL)
	mux.HandleFunc("GET /api/v1/sql/connections/{id}/schema", s.handleMySQLSchema)
	mux.HandleFunc("GET /api/v1/sql/connections/{id}/schema/health", s.handleMySQLSchemaHealth)
	mux.HandleFunc("GET /api/v1/sql/connections/{id}/processlist", s.handleMySQLProcesslist)
	mux.HandleFunc("GET /api/v1/sql/connections/{id}/locks", s.handleMySQLLocks)
	mux.HandleFunc("POST /api/v1/sql/connections/{id}/slow-sql/run", s.handleSlowSQLRun)
	mux.HandleFunc("GET /api/v1/sql/connections/{id}/slow-sql/latest", s.handleSlowSQLLatest)
	mux.HandleFunc("GET /api/v1/sql/connections/{id}/slow-sql/ps-limits", s.handleSlowSQLPSLimits)
	mux.HandleFunc("POST /api/v1/sql/connections/{id}/slow-sql/ps-limits/apply", s.handleSlowSQLApplyPSLimits)
	mux.HandleFunc("GET /api/v1/sql/slow-sql/reports", s.handleSlowSQLReports)
	mux.HandleFunc("GET /api/v1/sql/history", s.handleSQLQueryHistory)
	mux.HandleFunc("POST /api/v1/sql/history", s.handleAppendSQLQueryHistory)
	mux.HandleFunc("POST /api/v1/sql/change-requests", s.handleCreateSQLChangeRequest)
	mux.HandleFunc("GET /api/v1/sql/change-requests", s.handleListSQLChangeRequests)
	mux.HandleFunc("POST /api/v1/sql/change-requests/{id}/approve", s.handleApproveSQLChangeRequest)
	mux.HandleFunc("POST /api/v1/sql/change-requests/{id}/reject", s.handleRejectSQLChangeRequest)
	mux.HandleFunc("POST /api/v1/sql/change-requests/{id}/execute", s.handleExecuteSQLChangeRequest)
	// HTTP proxy auth token for window.open() scenarios
	mux.HandleFunc("GET /api/v1/proxy-token", s.handleProxyToken)
	// HTTP proxy: support all methods (GET/POST/PUT/DELETE/PATCH)
	mux.HandleFunc("GET /proxy/{hostID}/{port}/{path...}", s.handleHTTPProxy)
	mux.HandleFunc("POST /proxy/{hostID}/{port}/{path...}", s.handleHTTPProxy)
	mux.HandleFunc("PUT /proxy/{hostID}/{port}/{path...}", s.handleHTTPProxy)
	mux.HandleFunc("DELETE /proxy/{hostID}/{port}/{path...}", s.handleHTTPProxy)
	mux.HandleFunc("PATCH /proxy/{hostID}/{port}/{path...}", s.handleHTTPProxy)
	// Port forwarding: agent reverse channel (fingerprint-gated, not session-gated)
	mux.HandleFunc("GET /api/v1/agent/forward/wait", s.handleAgentForwardWait)
	mux.HandleFunc("GET /api/v1/agent/forward/rx", s.handleAgentForwardRx)
	mux.HandleFunc("POST /api/v1/agent/forward/tx", s.handleAgentForwardTx)
	// Hardware + NetFlow: agent ingest (fingerprint-gated)
	mux.HandleFunc("POST /api/v1/agent/hardware", s.handleAgentHardware)
	mux.HandleFunc("POST /api/v1/agent/netflow", s.handleAgentNetFlow)
	// SNMP: agent ingest (fingerprint-gated)
	mux.HandleFunc("POST /api/v1/agent/snmp", s.handleAgentSNMP)
	mux.HandleFunc("POST /api/v1/agent/snmp/trap", s.handleAgentSNMPTrap)
	mux.HandleFunc("POST /api/v1/agent/dnsmap", s.handleAgentDNSMap)
	mux.HandleFunc("POST /api/v1/agent/content-audit", s.handleAgentContentAudit)
	mux.HandleFunc("POST /api/v1/integrations/content-audit", s.handleGatewayContentAudit)
	mux.HandleFunc("GET /api/v1/content-audit/hosts", s.handleContentAuditHosts)
	mux.HandleFunc("GET /api/v1/content-audit", s.handleContentAudit)
	// Host security scan (Agent module + OSV)
	mux.HandleFunc("GET /api/v1/security/overview", s.handleSecurityOverview)
	mux.HandleFunc("POST /api/v1/security/host/scan", s.handleHostSecurityScan)
	mux.HandleFunc("GET /api/v1/security/host/scans", s.handleHostSecurityScans)
	mux.HandleFunc("GET /api/v1/security/host/scans/{id}", s.handleHostSecurityScanGet)
	mux.HandleFunc("POST /api/v1/security/host/scans/{id}/cancel", s.handleHostSecurityScanCancel)
	mux.HandleFunc("GET /api/v1/security/host/summary", s.handleHostSecuritySummary)
	mux.HandleFunc("GET /api/v1/security/host/config", s.handleGetHostSecurityConfig)
	mux.HandleFunc("POST /api/v1/security/host/config", s.handleSetHostSecurityConfig)
	mux.HandleFunc("GET /api/v1/security/findings/status", s.handleListSecurityFindingStates)
	mux.HandleFunc("POST /api/v1/security/findings/status", s.handleUpdateSecurityFindingState)
	// Web vulnerability scan (Nuclei)
	mux.HandleFunc("GET /api/v1/security/web/targets", s.handleListWebTargets)
	mux.HandleFunc("POST /api/v1/security/web/targets", s.handleUpsertWebTarget)
	mux.HandleFunc("PUT /api/v1/security/web/targets/{id}", s.handleUpsertWebTarget)
	mux.HandleFunc("DELETE /api/v1/security/web/targets/{id}", s.handleDeleteWebTarget)
	mux.HandleFunc("POST /api/v1/security/web/targets/{id}/scan", s.handleWebTargetScan)
	mux.HandleFunc("POST /api/v1/security/web/targets/import-openapi", s.handleImportWebOpenAPIScope)
	mux.HandleFunc("GET /api/v1/security/web/scans", s.handleWebScans)
	mux.HandleFunc("GET /api/v1/security/web/scans/{id}", s.handleWebScanGet)
	mux.HandleFunc("POST /api/v1/security/web/scans/{id}/cancel", s.handleWebScanCancel)
	mux.HandleFunc("GET /api/v1/security/web/config", s.handleGetWebSecurityConfig)
	mux.HandleFunc("POST /api/v1/security/web/config", s.handleSetWebSecurityConfig)
	mux.HandleFunc("GET /api/v1/security/web/engine", s.handleWebEngineStatus)
	mux.HandleFunc("POST /api/v1/security/web/engine/refresh", s.handleWebEngineRefresh)
	// Detection libraries (Nuclei templates, sqlmap signatures, payload/POC corpora)
	mux.HandleFunc("GET /api/v1/security/feeds", s.handleSecurityFeedStatus)
	mux.HandleFunc("POST /api/v1/security/feeds/config", s.handleSetSecurityFeedConfig)
	mux.HandleFunc("POST /api/v1/security/feeds/update", s.handleSecurityFeedUpdate)
	mux.HandleFunc("POST /api/v1/security/feeds/cancel", s.handleSecurityFeedCancel)
	mux.HandleFunc("POST /api/v1/security/feeds/test", s.handleSecurityFeedTest)
	// Hardware + NetFlow: frontend query
	mux.HandleFunc("GET /api/v1/hardware/health", s.handleHardwareHealth)
	mux.HandleFunc("GET /api/v1/hardware/history", s.handleHardwareHistory)
	mux.HandleFunc("GET /api/v1/hardware/events", s.handleHardwareEvents)
	mux.HandleFunc("DELETE /api/v1/hardware/{hostID}", s.handleDeleteHardware)
	// Hyper-V 虚拟机: agent ingest (fingerprint-gated) + frontend query
	mux.HandleFunc("POST /api/v1/agent/hyperv", s.handleAgentHyperV)
	mux.HandleFunc("GET /api/v1/hyperv/list", s.handleHyperVList)
	mux.HandleFunc("GET /api/v1/hyperv/events", s.handleHyperVEvents)
	mux.HandleFunc("POST /api/v1/hyperv/cleanup-duplicates", s.handleCleanupHyperVDuplicates)
	mux.HandleFunc("DELETE /api/v1/hyperv/{hostID}", s.handleDeleteHyperV)
	mux.HandleFunc("POST /api/v1/hyperv/{hostID}/guests/{vmID}/power", s.handleHyperVPower)
	mux.HandleFunc("POST /api/v1/hyperv/{hostID}/guests/{vmID}/config", s.handleHyperVConfig)
	mux.HandleFunc("POST /api/v1/agent/containers", s.handleAgentContainers)
	mux.HandleFunc("GET /api/v1/containers/list", s.handleContainerList)
	mux.HandleFunc("POST /api/v1/containers/{hostID}/{id}/action", s.handleContainerAction)
	mux.HandleFunc("GET /api/v1/containers/{hostID}/{id}/logs", s.handleContainerLogs)
	mux.HandleFunc("POST /api/v1/containers/{hostID}/{id}/exec", s.handleContainerExec)
	mux.HandleFunc("GET /api/v1/containers/{hostID}/{id}/terminal", s.handleContainerTerminal)
	mux.HandleFunc("GET /api/v1/containers/compose", s.handleContainerComposeList)
	mux.HandleFunc("POST /api/v1/containers/compose/{hostID}/action", s.handleContainerComposeAction)
	mux.HandleFunc("GET /api/v1/netflow/hosts", s.handleNetFlowHosts)
	mux.HandleFunc("GET /api/v1/netflow/summary", s.handleNetFlowSummary)
	mux.HandleFunc("GET /api/v1/netflow/ip-history", s.handleNetFlowIPHistory)
	mux.HandleFunc("GET /api/v1/netflow/flows", s.handleNetFlowFlows)
	mux.HandleFunc("GET /api/v1/netflow/packets", s.handleNetFlowPackets)
	// SNMP: frontend query
	mux.HandleFunc("GET /api/v1/snmp/hosts", s.handleSNMPHosts)
	mux.HandleFunc("GET /api/v1/snmp/list", s.handleSNMPList)
	mux.HandleFunc("DELETE /api/v1/snmp/{hostID}", s.handleDeleteSNMP)
	mux.HandleFunc("GET /api/v1/snmp/interface-history", s.handleSNMPInterfaceHistory)
	mux.HandleFunc("GET /api/v1/snmp/traps", s.handleSNMPTraps)
	mux.HandleFunc("GET /api/v1/hosts/meta", s.handleHostsMeta)
	mux.HandleFunc("GET /api/v1/install/info", s.handleInstallInfo)
	mux.HandleFunc("POST /api/v1/install/reset-token", s.handleResetToken)
	mux.HandleFunc("POST /api/v1/install/revoke-token", s.handleRevokeInstallToken)
	mux.HandleFunc("POST /api/v1/install/token-policy", s.handleSetInstallTokenPolicy)
	mux.HandleFunc("GET /install.sh", s.handleInstallScript)
	mux.HandleFunc("GET /install.ps1", s.handleInstallScript)
	mux.HandleFunc("GET /install-relay.sh", s.handleRelayInstallScript)
	mux.HandleFunc("GET /install-relay.ps1", s.handleRelayInstallScript)
	mux.HandleFunc("GET /uninstall.sh", s.handleUninstallScript)
	mux.HandleFunc("GET /uninstall.ps1", s.handleUninstallScript)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	// robots.txt：监控控制台不应被搜索引擎索引（内容需鉴权，索引 URL 也徒增暴露面）。
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
	})
	// P3-1: WebSocket push endpoint for real-time updates
	mux.HandleFunc("GET /ws/push", s.handlePushWS)
	mux.HandleFunc("GET /", s.handleDashboard)
	// static assets served from the embedded web/ dir
	if sub, err := fs.Sub(webFS, "web"); err == nil {
		fsrv := http.FileServer(http.FS(sub))
		// Serve style.css with no-cache (like /app.js). The raw FileServer set no
		// Cache-Control, so browsers HTTP-cached the CSS and kept showing an old
		// layout for a full deploy cycle after every UI tweak. no-cache forces a
		// revalidation so a redeploy is visible on the next load.
		mux.HandleFunc("GET /style.css", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			data, err := webFS.ReadFile("web/style.css")
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_, _ = w.Write(data)
		})
		// /app.js: 把 web/js/ 下的 8 个源模块按依赖顺序拼成【单个脚本】返回。
		// 必须作为单脚本加载——整文件函数提升(hoisting)才生效；若用 8 个独立
		// <script> 标签，早模块顶层调用晚模块里定义的 helper/handler 会因
		// 每脚本独立提升而 ReferenceError。源码保持拆分(便于维护)，运行时=单文件。
		mux.HandleFunc("GET /app.js", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			for _, m := range []string{"core", "export", "duplicates", "overview", "hosts", "host-picker", "forecast", "agent-update", "terminal", "desktop", "settings", "nav", "attachments", "sre", "host-inspect", "ai-assist", "ops-actions", "apimon", "governance", "datasource", "sql-toolkit", "cicd", "hardware", "hyperv", "containers", "k8s", "netflow", "snmp", "content-audit", "security-overview", "host-security", "security-feeds", "web-security", "security-center", "scrape", "dash_charts", "dashboard", "init"} {
				b, err := webFS.ReadFile("web/js/" + m + ".js")
				if err != nil {
					http.Error(w, "js module missing: "+m, http.StatusInternalServerError)
					return
				}
				_, _ = w.Write(b)
				_, _ = w.Write([]byte("\n;\n")) // 模块间安全分隔（空语句），防 ASI 边界问题
			}
		})
		mux.Handle("GET /theme-init.js", fsrv) // 主题预置（外置内联脚本，配合 CSP 去 unsafe-inline）
		mux.Handle("GET /i18n-dashboard.js", fsrv)
		mux.Handle("GET /i18n-dashboard.en.js", fsrv)
		mux.Handle("GET /i18n-dashboard.zh-TW.js", fsrv)
		// P2-1: support split CSS/JS modules
		// 注意：不能 StripPrefix——文件在 web/js、web/css 子目录下，需保留前缀映射到子目录。
		mux.Handle("GET /css/", fsrv)
		mux.Handle("GET /js/", fsrv)
		// Former Vue SPA paths stay closed (404) — classic UI only.
		mux.HandleFunc("GET /v2/", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
		mux.HandleFunc("GET /v2", func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
		mux.Handle("GET /manifest.json", fsrv)
		mux.Handle("GET /icon.svg", fsrv)
		// Service Worker: needs Service-Worker-Allowed header for root scope control
		mux.HandleFunc("GET /sw.js", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Header().Set("Service-Worker-Allowed", "/")
			w.Header().Set("Cache-Control", "no-cache")
			data, err := webFS.ReadFile("web/sw.js")
			if err != nil {
				http.Error(w, "not found", 404)
				return
			}
			w.Write(data)
		})
	}
	// agent binaries + plugins.zip for the one-line install command
	if s.distDir != "" {
		mux.HandleFunc("GET /dl/", s.handleDownload)
	}
	return mux
}

// handleDownload serves agent binaries / plugins.zip from distDir with strong
// caching so re-installs and多机 installs don't re-download the full 7.5MB every
// time. http.ServeContent 负责 Range(断点续传)+If-None-Match/If-Modified-Since
// (条件 GET→304)，我们只需补上 ETag 与 Cache-Control：
//   - ETag=size-mtime 指纹：内容不变则客户端/CDN 命中 304，只回 header 不回 body。
//   - Cache-Control: public,max-age —— 让 CDN/relay 边缘缓存；用 max-age+ETag 而非
//     immutable，因为发版后同名 URL 内容会变，必须允许重新校验才能拿到新版 agent。
//
// gzip 中间件已对 /dl/ 前缀 bypass（二进制本就是压缩态，再 gzip 无益且破坏 Range）。
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/dl/")
	if name == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	checksumOnly := strings.HasSuffix(name, ".sha256")
	if checksumOnly {
		name = strings.TrimSuffix(name, ".sha256")
		if name == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	}
	// 防目录穿越：Clean("/"+name) 消解 ../，再 Join 到 distDir。
	full := filepath.Join(s.distDir, filepath.Clean("/"+name))
	f, err := os.Open(full)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if checksumOnly {
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			http.Error(w, "checksum failed", http.StatusInternalServerError)
			return
		}
		sum := fmt.Sprintf("%x", h.Sum(nil))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("ETag", `"`+sum+`"`)
		_, _ = fmt.Fprintf(w, "%s  %s\n", sum, filepath.Base(name))
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, fi.Size(), fi.ModTime().UnixNano()))
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
