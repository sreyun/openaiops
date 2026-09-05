package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RetentionConfig controls daily cleanup windows (days). Zero = use defaults.
type RetentionConfig struct {
	AuditDays        int `json:"audit_days,omitempty"`         // audit_log + events
	AlertHistoryDays int `json:"alert_history_days,omitempty"` // alert_history
	ContentAuditDays int `json:"content_audit_days,omitempty"` // content_audit
	MemoryDays       int `json:"memory_days,omitempty"`        // soft age for memory cleanup
	NetFlowMonths    int `json:"netflow_months,omitempty"`     // drop partitions older than N months
	AICallDays       int `json:"ai_call_days,omitempty"`       // AI 调用与人工反馈观测
	// OpsHistoryDays 覆盖会话 / Run / 剧本与自愈执行 / Trap / 硬件与 Hyper-V 事件等
	// 可再生的运行历史。这些表此前没有任何清理，是 PG 无界增长的主要来源之一。
	OpsHistoryDays int `json:"ops_history_days,omitempty"`
}

func (r RetentionConfig) withDefaults() RetentionConfig {
	if r.AuditDays <= 0 {
		r.AuditDays = 180
	}
	if r.AlertHistoryDays <= 0 {
		r.AlertHistoryDays = 90
	}
	if r.ContentAuditDays <= 0 {
		r.ContentAuditDays = 30
	}
	if r.MemoryDays <= 0 {
		r.MemoryDays = 365
	}
	if r.NetFlowMonths <= 0 {
		r.NetFlowMonths = 12
	}
	if r.AICallDays <= 0 {
		r.AICallDays = 365
	}
	if r.OpsHistoryDays <= 0 {
		r.OpsHistoryDays = 90
	}
	return r
}

// BackupConfig schedules PostgreSQL dumps via pg_dump.
type BackupConfig struct {
	Enabled     bool               `json:"enabled"`
	DailyAt     string             `json:"daily_at,omitempty"` // HH:MM local, default 02:30
	RetainCount int                `json:"retain_count,omitempty"`
	Dir         string             `json:"dir,omitempty"` // override AIOPS_BACKUP_DIR
	Remote      BackupRemoteConfig `json:"remote,omitempty"`
	// 灾备的另一半：时序与录像。默认关闭是刻意的——导出时序会占磁盘，
	// 得让运维明确知道自己开了什么。见 backup_full.go。
	IncludeVM         bool `json:"include_vm,omitempty"`
	VMDays            int  `json:"vm_days,omitempty"` // 导出最近 N 天时序，默认 90
	IncludeRecordings bool `json:"include_recordings,omitempty"`
}

func (b BackupConfig) withDefaults() BackupConfig {
	if b.DailyAt == "" {
		b.DailyAt = "02:30"
	}
	if b.RetainCount <= 0 {
		b.RetainCount = 14
	}
	if b.VMDays <= 0 {
		b.VMDays = 90
	}
	return b
}

type BackupMeta struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	Operator  string `json:"operator"`
	Path      string `json:"path"`
	Note      string `json:"note,omitempty"`
}

func (cs *ConfigStore) Retention() RetentionConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.cfg.Retention.withDefaults()
}

func (cs *ConfigStore) BackupCfg() BackupConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.cfg.Backup.withDefaults()
}

func (cs *ConfigStore) CmdPolicy() CmdPolicyConfig {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	p := cs.cfg.CmdPolicy
	if p.Mode == "" {
		p.Mode = "strict"
	}
	return p
}

func (cs *ConfigStore) SetRetention(r RetentionConfig) error {
	cs.mu.Lock()
	cs.cfg.Retention = r.withDefaults()
	cs.mu.Unlock()
	return cs.save()
}

func (cs *ConfigStore) SetBackupCfg(b BackupConfig) error {
	cs.mu.Lock()
	// Preserve remote secret when blank/masked.
	if b.Remote.SecretKey == "" || strings.Contains(b.Remote.SecretKey, "****") {
		b.Remote.SecretKey = cs.cfg.Backup.Remote.SecretKey
	}
	if b.Remote.AccessKey == "" || strings.Contains(b.Remote.AccessKey, "****") {
		b.Remote.AccessKey = cs.cfg.Backup.Remote.AccessKey
	}
	cs.cfg.Backup = b.withDefaults()
	cs.mu.Unlock()
	return cs.save()
}

func (cs *ConfigStore) SetCmdPolicy(p CmdPolicyConfig) error {
	if p.Mode == "" {
		p.Mode = "strict"
	}
	cs.mu.Lock()
	cs.cfg.CmdPolicy = p
	cs.mu.Unlock()
	return cs.save()
}

func backupDir(cfg BackupConfig) string {
	if d := strings.TrimSpace(cfg.Dir); d != "" {
		return d
	}
	if d := strings.TrimSpace(os.Getenv("AIOPS_BACKUP_DIR")); d != "" {
		return d
	}
	return filepath.Join(".", "backups")
}

// backupCreateMu serializes stamp allocation + O_EXCL reservation only (not the
// long-running dump/export). Without unique stamps, a manual click colliding with
// the daily scheduler in the same wall-clock second shared one second-precision
// filename, and O_TRUNC / pg_dump --file silently destroyed the other writer's
// artifact while both returned success.
var (
	backupCreateMu sync.Mutex
	backupStampSeq uint32
)

// uniqueBackupStamp returns a filesystem-safe stamp that stays unique under concurrent
// callers even when wall-clock seconds collide. Format: YYYYMMDD-HHMMSS-<seq6>.
// Caller must hold backupCreateMu.
func uniqueBackupStamp() string {
	n := backupStampSeq
	backupStampSeq++
	now := time.Now()
	return fmt.Sprintf("%s-%06d", now.Format("20060102-150405"), (uint32(now.Nanosecond()/1000)+n)%1000000)
}

// reserveBackupArtifact creates an exclusive empty file under dir named
// prefix+stamp+suffix and returns (id, path). Holds the mutex only for the
// reservation so concurrent PG/VM/recordings backups can run in parallel safely.
func reserveBackupArtifact(dir, prefix, suffix string) (id, path string, err error) {
	backupCreateMu.Lock()
	defer backupCreateMu.Unlock()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", err
	}
	for attempt := 0; attempt < 8; attempt++ {
		id = prefix + uniqueBackupStamp() + suffix
		path = filepath.Join(dir, id)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return id, path, nil
		}
		if !os.IsExist(err) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("预占备份文件失败: exhausted unique names")
}

// pgToolStatus probes one PostgreSQL client tool (pg_dump / pg_restore) so the
// UI can surface availability before the user triggers a backup/restore.
func pgToolStatus(tool string) map[string]any {
	out := map[string]any{tool + "_ok": false}
	path, err := exec.LookPath(tool)
	if err != nil {
		out[tool+"_error"] = pgToolMissingHint(tool)
		return out
	}
	out[tool+"_path"] = path
	if b, err := exec.Command(path, "--version").CombinedOutput(); err == nil {
		out[tool+"_version"] = strings.TrimSpace(string(b))
	}
	out[tool+"_ok"] = true
	return out
}

// pgToolMissingHint returns an actionable hint depending on the deployment mode.
func pgToolMissingHint(tool string) string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return fmt.Sprintf("容器内未找到 %s，请更新服务端镜像（新版镜像已内置 PostgreSQL 客户端工具）", tool)
	}
	return fmt.Sprintf("未找到 %s，请安装 PostgreSQL 客户端工具（如 Debian: apt install postgresql-client，CentOS: yum install postgresql）", tool)
}

// backupToolsStatus aggregates client tool availability for the settings UI.
func backupToolsStatus() map[string]any {
	out := map[string]any{}
	for k, v := range pgToolStatus("pg_dump") {
		out[k] = v
	}
	for k, v := range pgToolStatus("pg_restore") {
		out[k] = v
	}
	return out
}

func (s *Server) listBackups() ([]BackupMeta, error) {
	if s.pg == nil {
		return nil, fmt.Errorf("PostgreSQL 未启用")
	}
	rows, err := s.pg.db.Query(`SELECT id, created_at, size_bytes, sha256, operator, path, note FROM backup_meta ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		// table may not exist yet on very old failed migrate — fall back to filesystem
		return s.listBackupsFS()
	}
	defer rows.Close()
	var out []BackupMeta
	for rows.Next() {
		var m BackupMeta
		if err := rows.Scan(&m.ID, &m.CreatedAt, &m.SizeBytes, &m.SHA256, &m.Operator, &m.Path, &m.Note); err != nil {
			return out, err
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return s.listBackupsFS()
	}
	return out, rows.Err()
}

func (s *Server) listBackupsFS() ([]BackupMeta, error) {
	dir := backupDir(s.cfg.BackupCfg())
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []BackupMeta
	for _, e := range ents {
		if e.IsDir() || !(strings.HasSuffix(e.Name(), ".dump") ||
			strings.HasSuffix(e.Name(), ".native.gz") || strings.HasSuffix(e.Name(), ".tar.gz")) {
			continue
		}
		info, _ := e.Info()
		var sz int64
		var mod time.Time
		if info != nil {
			sz = info.Size()
			mod = info.ModTime()
		}
		out = append(out, BackupMeta{
			ID: e.Name(), CreatedAt: mod.Unix(), SizeBytes: sz,
			Path: filepath.Join(dir, e.Name()), Operator: "fs",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

func (s *Server) createPGBackup(operator, note string) (BackupMeta, error) {
	dsn := strings.TrimSpace(os.Getenv("AIOPS_POSTGRES_DSN"))
	if dsn == "" {
		s.cfg.mu.RLock()
		dsn = s.cfg.cfg.PostgresDSN
		s.cfg.mu.RUnlock()
	}
	if dsn == "" {
		return BackupMeta{}, fmt.Errorf("未配置 PostgreSQL DSN")
	}
	cfg := s.cfg.BackupCfg()
	if _, err := exec.LookPath("pg_dump"); err != nil {
		return BackupMeta{}, fmt.Errorf("%s（备份目录：%s）", pgToolMissingHint("pg_dump"), backupDir(cfg))
	}
	dir := backupDir(cfg)
	id, path, err := reserveBackupArtifact(dir, backupPrefixPG, ".dump")
	if err != nil {
		return BackupMeta{}, err
	}
	cmd := exec.Command("pg_dump", "--format=custom", "--file", path, dsn)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(path)
		return BackupMeta{}, fmt.Errorf("pg_dump 失败: %v (%s)", err, truncateRunes(string(out), 400))
	}
	sum, size, err := fileSHA256(path)
	if err != nil {
		return BackupMeta{}, err
	}
	meta := BackupMeta{
		ID: id, CreatedAt: time.Now().Unix(), SizeBytes: size,
		SHA256: sum, Operator: operator, Path: path, Note: note,
	}
	if s.pg != nil {
		_, _ = s.pg.db.Exec(`INSERT INTO backup_meta(id, created_at, size_bytes, sha256, operator, path, note)
			VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (id) DO UPDATE SET size_bytes=EXCLUDED.size_bytes, sha256=EXCLUDED.sha256`,
			meta.ID, meta.CreatedAt, meta.SizeBytes, meta.SHA256, meta.Operator, meta.Path, meta.Note)
	}
	if cfg.Remote.Enabled {
		if err := s.uploadBackupRemote(path, id); err != nil {
			slog.Error("remote backup upload failed", "err", err, "id", id)
			meta.Note = strings.TrimSpace(meta.Note + " remote_upload_error:" + err.Error())
		} else if meta.Note == "" {
			meta.Note = "remote_ok"
		} else {
			meta.Note += ";remote_ok"
		}
	}
	s.pruneBackups(cfg.RetainCount)
	return meta, nil
}

// pruneBackups 按**种类**各留 retain 份。混在一起排序会出现"新做的 VM 备份被一串
// PG 备份挤出保留窗口"——那等于时序备份开了等于没开。
func (s *Server) pruneBackups(retain int) {
	if retain <= 0 {
		return
	}
	list, err := s.listBackups()
	if err != nil {
		// 台账读不到（PG 未就绪/表还没建）不该让保留策略整个失效——
		// 备份文件本身在盘上，按文件系统裁剪照样是对的。
		if list, err = s.listBackupsFS(); err != nil {
			return
		}
	}
	kept := map[string]int{}
	for _, m := range list { // listBackups 已按 created_at 倒序
		kind := backupKindOf(m.ID)
		kept[kind]++
		if kept[kind] <= retain {
			continue
		}
		_ = os.Remove(m.Path)
		if s.pg != nil {
			_, _ = s.pg.db.Exec(`DELETE FROM backup_meta WHERE id=$1`, m.ID)
		}
	}
}

// restorePGBackup restores a custom-format dump using the drop-and-recreate
// strategy. The legacy approach (pg_restore --clean --if-exists into the live DB)
// fails on partitioned tables: clean-mode DROP CONSTRAINT statements against
// partition children (e.g. flow_records_default_pkey) are rejected because those
// constraints are inherited from the parent partition and cannot be dropped
// directly. Recreating an empty database sidesteps every DROP and makes the
// restore deterministic. A safety dump of the current state is taken first.
func (s *Server) restorePGBackup(id, operator string) error {
	list, err := s.listBackups()
	if err != nil {
		return err
	}
	var meta *BackupMeta
	for i := range list {
		if list[i].ID == id {
			meta = &list[i]
			break
		}
	}
	if meta == nil {
		return fmt.Errorf("备份不存在")
	}
	// 时序/录像备份不是 pg_dump 产物，喂给 pg_restore 只会得到一条难懂的报错，
	// 而这条路径**会先删库**——必须在删库之前拦住。
	if backupKindOf(meta.ID) != "postgres" {
		return fmt.Errorf("该备份是 %s 类型，不能用 PostgreSQL 还原流程；请按 docs/DEPLOY_GUIDE.md 的对应章节还原", backupKindOf(meta.ID))
	}
	if _, err := os.Stat(meta.Path); err != nil {
		return fmt.Errorf("备份文件缺失: %w", err)
	}
	if _, err := exec.LookPath("pg_restore"); err != nil {
		return fmt.Errorf("%s", pgToolMissingHint("pg_restore"))
	}
	dsn := strings.TrimSpace(os.Getenv("AIOPS_POSTGRES_DSN"))
	if dsn == "" {
		s.cfg.mu.RLock()
		dsn = s.cfg.cfg.PostgresDSN
		s.cfg.mu.RUnlock()
	}
	if strings.TrimSpace(dsn) == "" {
		return fmt.Errorf("未配置 PostgreSQL DSN")
	}
	// 1) 破坏性操作前先打一份保护性备份，还原失败也能找回当前状态。
	safety, err := s.createPGBackup(operator, "pre-restore safety backup")
	if err != nil {
		return fmt.Errorf("还原前保护性备份失败（已中止还原）: %w", err)
	}
	// 2) 连接维护库 postgres，删除并重建目标库（FORCE 断开存量连接，含服务端自身连接池）。
	if err := pgRecreateDatabase(dsn); err != nil {
		return fmt.Errorf("重建数据库失败: %w", err)
	}
	// 3) 向空库还原（无需 --clean，库内无任何对象）。
	cmd := exec.Command("pg_restore", "--no-owner", "--exit-on-error", "--dbname", dsn, meta.Path)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_restore 失败: %v (%s)", err, truncateRunes(string(out), 600))
	}
	// 4) 保护性备份的元数据产生于被还原的旧库之后，重建后需补写，避免列表丢失。
	if s.pg != nil {
		_, _ = s.pg.db.Exec(`INSERT INTO backup_meta(id, created_at, size_bytes, sha256, operator, path, note)
			VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (id) DO UPDATE SET size_bytes=EXCLUDED.size_bytes, sha256=EXCLUDED.sha256`,
			safety.ID, safety.CreatedAt, safety.SizeBytes, safety.SHA256, safety.Operator, safety.Path, safety.Note)
	}
	slog.Info("PostgreSQL restore completed (drop-and-recreate)", "backup", id, "operator", operator)
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "warning", Actor: operator, Message: "从备份还原 PostgreSQL（删库重建）：" + id})
	return nil
}

// pgSplitMaintenanceDSN splits a postgres:// DSN into (maintenance DSN pointing
// at the "postgres" database, target database name).
func pgSplitMaintenanceDSN(dsn string) (maint string, dbName string, err error) {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return "", "", err
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", "", fmt.Errorf("仅支持 postgres:// 形式的 DSN（当前 scheme=%q），无法定位维护库", u.Scheme)
	}
	dbName = strings.TrimPrefix(u.Path, "/")
	if dbName == "" {
		return "", "", fmt.Errorf("DSN 未包含目标数据库名")
	}
	mu := *u
	mu.Path = "/postgres"
	return mu.String(), dbName, nil
}

// pgRecreateDatabase drops and re-creates the target database via the
// "postgres" maintenance database. WITH (FORCE) (PG≥13) terminates remaining
// sessions — including this server's own pool, which reconnects lazily.
func pgRecreateDatabase(dsn string) error {
	maint, dbName, err := pgSplitMaintenanceDSN(dsn)
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", maint)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("连接维护库失败: %w", err)
	}
	qi := pqQuoteIdent(dbName)
	if _, err := db.ExecContext(ctx, "DROP DATABASE IF EXISTS "+qi+" WITH (FORCE)"); err != nil {
		// FORCE 需要 PG≥13；旧版本退化为普通 DROP（要求无活动连接）。
		if !strings.Contains(strings.ToLower(err.Error()), "syntax error") {
			return err
		}
		if _, err2 := db.ExecContext(ctx, "DROP DATABASE IF EXISTS "+qi); err2 != nil {
			return err2
		}
	}
	if _, err := db.ExecContext(ctx, "CREATE DATABASE "+qi); err != nil {
		return err
	}
	return nil
}

// pqQuoteIdent quotes an SQL identifier (pgx-style, no external dependency).
func pqQuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func maskEmail(s string) string {
	s = strings.TrimSpace(s)
	at := strings.IndexByte(s, '@')
	if at <= 1 {
		return "***"
	}
	return s[:1] + "***" + s[at:]
}

func maskPhone(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 7 {
		return "***"
	}
	return s[:3] + "****" + s[len(s)-4:]
}

var backupSchedOnce sync.Once

func (s *Server) startBackupScheduler() {
	backupSchedOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			var lastDay string
			for range ticker.C {
				cfg := s.cfg.BackupCfg()
				if !cfg.Enabled {
					continue
				}
				now := time.Now()
				want := cfg.DailyAt
				hhmm := now.Format("15:04")
				day := now.Format("2006-01-02")
				if hhmm == want && lastDay != day {
					lastDay = day
					if _, err := s.createPGBackup("scheduler", "scheduled"); err != nil {
						slog.Error("scheduled PG backup failed", "err", err)
					} else {
						slog.Info("scheduled PG backup ok")
					}
					if cfg.IncludeVM {
						if m, err := s.createVMBackup("scheduler", "scheduled"); err != nil {
							slog.Error("定时时序备份失败", "err", err)
						} else {
							slog.Info("定时时序备份完成", "id", m.ID, "size", m.SizeBytes)
						}
					}
					if cfg.IncludeRecordings {
						if m, err := s.createRecordingsBackup("scheduler", "scheduled"); err != nil {
							slog.Error("定时录像备份失败", "err", err)
						} else {
							slog.Info("定时录像备份完成", "id", m.ID, "size", m.SizeBytes)
						}
					}
				}
			}
		}()
	})
}

// --- HTTP handlers (admin only via routeAllowed on /api/v1/admin/*) ---

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	list, err := s.listBackups()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	meta, err := s.createPGBackup(s.actorName(r), "manual")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.store.AddLog(LogEntry{Kind: KindOperation, Level: "info", Actor: s.actorName(r), IP: s.clientIP(r), Message: "创建 PostgreSQL 备份：" + meta.ID})
	writeJSON(w, http.StatusOK, meta)
}

func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	list, err := s.listBackups()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	for _, m := range list {
		if m.ID != id {
			continue
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename=\""+m.ID+"\"")
		http.ServeFile(w, r, m.Path)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "备份不存在"})
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Confirm string `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Confirm != "RESTORE" && in.Confirm != id {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请在 confirm 字段填写 RESTORE 或备份 ID 以二次确认"})
		return
	}
	if err := s.restorePGBackup(id, s.actorName(r)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hint": "还原已执行（删库重建模式，还原前已自动创建保护性备份），建议重启服务端进程以重新加载内存状态"})
}

func (s *Server) handleGetRetention(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Retention())
}

func (s *Server) handleSetRetention(w http.ResponseWriter, r *http.Request) {
	var in RetentionConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.cfg.SetRetention(in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "retention": s.cfg.Retention()})
}

func (s *Server) handleGetBackupCfg(w http.ResponseWriter, r *http.Request) {
	b := s.cfg.BackupCfg()
	b.Remote.SecretKey = maskSecret(b.Remote.SecretKey)
	b.Remote.AccessKey = maskSecret(b.Remote.AccessKey)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":       b.Enabled,
		"daily_at":      b.DailyAt,
		"retain_count":  b.RetainCount,
		"dir":           b.Dir,
		"remote":        b.Remote,
		"dir_effective": backupDir(b),
		"tools":         backupToolsStatus(),
	})
}

func (s *Server) handleSetBackupCfg(w http.ResponseWriter, r *http.Request) {
	var in BackupConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.cfg.SetBackupCfg(in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "backup": s.cfg.BackupCfg()})
}

func (s *Server) handleGetCmdPolicy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.CmdPolicy())
}

func (s *Server) handleSetCmdPolicy(w http.ResponseWriter, r *http.Request) {
	var in CmdPolicyConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": Tr(r, "common.invalid_json")})
		return
	}
	if err := s.cfg.SetCmdPolicy(in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cmd_policy": s.cfg.CmdPolicy()})
}
