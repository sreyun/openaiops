package main

// 一键诊断包。
//
// 出问题时的现状是「远程登录客户的机器现场翻」——要 VPN、要审批、要人陪同，一次
// 往返半天。诊断包把售后第一轮要问的东西一次性打包：版本、脱敏配置、迁移版本、
// PG/VM 连通性、最近活动日志、平台自身故障、goroutine 快照与实时指标。
//
// 三条边界：
//   - **只走脱敏后的配置**（sanitizedConfig），与 GET /api/v1/config 同一份打码口径；
//     诊断包会被邮件转发、贴进工单，绝不能带出密钥。
//   - **不含任何业务数据**：不打包指标、日志正文、终端录像、审计明细。要的是"平台
//     自身状态"，不是客户的数据。
//   - **admin only**（走 /api/v1/admin/ 前缀自动收敛）+ 审计留痕：谁在什么时候导出过。

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"time"
)

// supportBundleEnvSafe 是可以原样带出的环境变量（不含凭据）。其余 AIOPS_* 只报
// 「是否已设置」，值一律不带。
var supportBundleEnvSafe = map[string]bool{
	"AIOPS_VM_URL":              true,
	"AIOPS_LICENSE_ENFORCE":     true,
	"AIOPS_LICENSE_FILE":        true,
	"AIOPS_LOG_LEVEL":           true,
	"AIOPS_HISTORY_SOURCE":      true,
	"AIOPS_DISABLE_AUTO_UPDATE": true,
	"TZ":                        true,
}

// handleSupportBundle GET /api/v1/admin/support-bundle —— 下载诊断包（admin）。
func (s *Server) handleSupportBundle(w http.ResponseWriter, r *http.Request) {
	ts := time.Now()
	name := fmt.Sprintf("aiops-support-%s-%s.zip", sanitizeFileToken(appVersion), ts.Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	w.Header().Set("Cache-Control", "no-store")

	zw := zip.NewWriter(w)
	add := func(fname string, body []byte) {
		f, err := zw.Create(fname)
		if err != nil {
			return
		}
		_, _ = f.Write(body)
	}
	addJSON := func(fname string, v any) {
		raw, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			raw = []byte(fmt.Sprintf("{\"error\":%q}", err.Error()))
		}
		add(fname, raw)
	}

	add("README.txt", []byte(supportBundleReadme(ts)))
	addJSON("meta.json", s.supportMeta(ts))
	addJSON("config.sanitized.json", s.sanitizedConfig())
	addJSON("license.json", s.licenseStatus())
	addJSON("connectivity.json", s.supportConnectivity())
	addJSON("schema_migrations.json", s.supportSchemaVersions())
	addJSON("hosts.json", s.supportHosts())
	addJSON("platform_faults.json", s.faults.snapshot(50))
	addJSON("env.json", supportEnv())
	add("activity.log", []byte(s.supportActivity()))
	add("goroutines.txt", supportGoroutines())
	add("metrics.txt", []byte(s.supportMetricsText(r)))

	if err := zw.Close(); err != nil {
		return // 头已经发出去了，这里只能中断；客户端会看到不完整的 zip
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info", Message: Tz("log.support_bundle_export")})
}

func supportBundleReadme(ts time.Time) string {
	return strings.Join([]string{
		"AIOps 诊断包",
		"生成时间：" + ts.Format("2006-01-02 15:04:05"),
		"",
		"这个包用于售后定位问题，可直接发给技术支持。内容：",
		"  meta.json               版本 / 运行时长 / 部署指纹 / 主机与告警计数",
		"  config.sanitized.json   平台配置（所有密钥、令牌、DSN 已打码）",
		"  license.json            授权状态与用量",
		"  connectivity.json       PostgreSQL / VictoriaMetrics 连通性与熔断状态",
		"  schema_migrations.json  已应用的数据库迁移版本（升级问题看这个）",
		"  hosts.json              主机清单（仅身份与在线状态，不含任何指标）",
		"  platform_faults.json    平台自身故障（panic / 自诊断）",
		"  env.json                AIOPS_* 环境变量（凭据只标记是否设置，不带值）",
		"  activity.log            最近活动日志",
		"  goroutines.txt          goroutine 快照（卡死 / 泄漏问题看这个）",
		"  metrics.txt             /metrics 的一次快照",
		"",
		"不包含：指标数据、日志正文、终端/桌面录像、审计明细、任何密钥。",
	}, "\n") + "\n"
}

func (s *Server) supportMeta(ts time.Time) map[string]any {
	hostname, _ := os.Hostname()
	hosts := s.store.ListHosts()
	th := s.cfg.Thresholds()
	offlineAfter := int64(th.OfflineAfter.Seconds())
	now := ts.Unix()
	online := 0
	for _, h := range hosts {
		if now-h.LastSeen <= offlineAfter {
			online++
		}
	}
	licMu.RLock()
	install := licInstallID
	licMu.RUnlock()
	return map[string]any{
		"generated_at":   ts.Format(time.RFC3339),
		"version":        appVersion,
		"install_id":     install,
		"go_version":     runtime.Version(),
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"num_cpu":        runtime.NumCPU(),
		"goroutines":     runtime.NumGoroutine(),
		"uptime_seconds": int64(time.Since(metricProcessStart).Seconds()),
		"server_host":    hostname,
		"hosts_total":    len(hosts),
		"hosts_online":   online,
	}
}

func (s *Server) supportConnectivity() map[string]any {
	out := map[string]any{}
	if s.pg != nil && s.pg.db != nil {
		start := time.Now()
		err := s.pg.db.Ping()
		pg := map[string]any{
			"reachable":  err == nil,
			"ping_ms":    time.Since(start).Milliseconds(),
			"pool_stats": s.pg.db.Stats(),
		}
		if err != nil {
			pg["error"] = err.Error()
		}
		var ver string
		if e := s.pg.db.QueryRow(`SELECT version()`).Scan(&ver); e == nil {
			pg["server_version"] = ver
		}
		out["postgres"] = pg
	} else {
		out["postgres"] = map[string]any{"reachable": false, "error": "no PG store bound"}
	}
	if s.vm != nil {
		c := s.cfg.VMConfig()
		vm := map[string]any{
			"enabled":         c.Enabled,
			"url":             c.URL,
			"write_breaker":   s.vm.writeBreaker().state(),
			"read_breaker":    s.vm.queryBreaker().state(),
			"dropped_samples": s.vm.dropped.Load(),
			"queue_depth": map[string]int{
				"host": len(s.vm.ch), "check": len(s.vm.checkCh),
				"api": len(s.vm.apiCh), "raw": len(s.vm.rawCh),
			},
		}
		for k, v := range s.vm.diag.snapshot() {
			vm[k] = v
		}
		out["victoriametrics"] = vm
	}
	out["pg_flush"] = map[string]any{
		"rounds":     pgFlushCount.Load(),
		"heavy":      pgFlushHeavyCnt.Load(),
		"last_ms":    pgFlushLastMs.Load(),
		"max_ms":     pgFlushMaxMs.Load(),
		"last_ts":    pgFlushLastTS.Load(),
		"last_human": humanTS(pgFlushLastTS.Load()),
	}
	return out
}

func humanTS(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}

// supportSchemaVersions 读已应用的迁移版本。升级出问题时第一句话就是
// 「你们那套跑到第几号迁移了」——没有这个只能猜。
func (s *Server) supportSchemaVersions() any {
	if s.pg == nil || s.pg.db == nil {
		return map[string]any{"error": "no PG store bound"}
	}
	rows, err := s.pg.db.Query(`SELECT version, name, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer rows.Close()
	out := []map[string]any{}
	var scanErr string
	for rows.Next() {
		var ver int
		var name string
		var applied time.Time // 列是 TIMESTAMPTZ，别按 unix 秒扫——扫失败就是一份空清单
		if err := rows.Scan(&ver, &name, &applied); err != nil {
			scanErr = err.Error()
			continue
		}
		out = append(out, map[string]any{
			"version": ver, "name": name, "applied_at": applied.Format("2006-01-02 15:04:05"),
		})
	}
	if err := rows.Err(); err != nil {
		return map[string]any{"error": err.Error(), "partial": out}
	}
	if scanErr != "" && len(out) == 0 {
		// 静默返回空清单是最坏的结果：售后会以为"这套确实没跑过迁移"。
		return map[string]any{"error": scanErr}
	}
	return out
}

// supportHosts 只带身份与在线状态：诊断包不该顺手把客户的指标数据带出去。
func (s *Server) supportHosts() []map[string]any {
	hosts := s.store.ListHosts()
	out := make([]map[string]any, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, map[string]any{
			"id":            shortID(h.ID),
			"hostname":      h.Hostname,
			"os":            h.OS,
			"agent_version": h.AgentVersion,
			"last_seen":     humanTS(h.LastSeen),
		})
	}
	return out
}

func (s *Server) supportActivity() string {
	entries := s.store.RecentActivity()
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n",
			humanTS(e.Timestamp), e.Level, e.Kind, e.Actor, strings.ReplaceAll(e.Message, "\n", " "))
	}
	return b.String()
}

func supportEnv() map[string]string {
	out := map[string]string{}
	keys := make([]string, 0, 32)
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := kv[:i]
		if !strings.HasPrefix(k, "AIOPS_") && k != "TZ" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := os.Getenv(k)
		if supportBundleEnvSafe[k] {
			out[k] = v
			continue
		}
		if strings.TrimSpace(v) == "" {
			out[k] = "(empty)"
		} else {
			out[k] = "(set, masked)"
		}
	}
	return out
}

func supportGoroutines() []byte {
	var b strings.Builder
	if p := pprof.Lookup("goroutine"); p != nil {
		_ = p.WriteTo(&stringWriter{&b}, 1)
	}
	return []byte(b.String())
}

// stringWriter 把 strings.Builder 适配成 io.Writer（pprof 需要 io.Writer）。
type stringWriter struct{ b *strings.Builder }

func (w *stringWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

// supportMetricsText 把 /metrics 的当前快照塞进包里，省得售后再要一次抓取。
func (s *Server) supportMetricsText(r *http.Request) string {
	rec := newBufferedResponse()
	req := r.Clone(r.Context())
	req.URL.Path = "/metrics"
	req.Header.Set("Authorization", "") // 走会话分支：调用方已经是 admin
	s.handleMetrics(rec, req)
	return rec.body.String()
}

// bufferedResponse 是给内部调用用的极小 ResponseWriter。
type bufferedResponse struct {
	header http.Header
	body   *strings.Builder
	code   int
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: http.Header{}, body: &strings.Builder{}, code: 200}
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *bufferedResponse) WriteHeader(code int)        { b.code = code }

func sanitizeFileToken(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		return "aiops"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
