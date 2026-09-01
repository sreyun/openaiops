package main

// 灾备的另一半。
//
// backup.go 只做 pg_dump，DEPLOY_GUIDE 里也白纸黑字写着「不含 VictoriaMetrics 与
// 录像目录」。也就是说客户真出事，丢的是过去 90 天的曲线和终端录像——后者恰恰是
// 审计取证时唯一有价值的东西。这里把这两半补齐：
//
//   - **时序**：走 VM 的 /api/v1/export/native（HTTP 导出，不需要能访问 VM 的数据目录，
//     容器化部署下这是唯一可行的路径），落成 .native.gz；还原用 /api/v1/import/native。
//   - **录像**：终端与桌面两个录制目录打成 .tar.gz。
//
// 三种产物共用同一个备份目录与 backup_meta 台账，用文件名前缀区分种类；
// 保留策略**按种类各留 N 份**——混在一起排序会出现"VM 备份被一串 PG 备份挤掉"。

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	backupPrefixPG  = "aiops-pg-"
	backupPrefixVM  = "aiops-vm-"
	backupPrefixRec = "aiops-rec-"
)

// backupKindOf 按文件名前缀判定种类（保留策略与还原准入都依赖它）。
func backupKindOf(id string) string {
	switch {
	case strings.HasPrefix(id, backupPrefixVM):
		return "vm"
	case strings.HasPrefix(id, backupPrefixRec):
		return "recordings"
	case strings.HasPrefix(id, backupPrefixPG):
		return "postgres"
	default:
		return "postgres" // 历史文件名（aiops-pg-*.dump 之前的产物）按 PG 处理
	}
}

// vmExportClient 是专给备份用的 HTTP 客户端：导出可能跑几十分钟，
// 不能复用带短超时的常规客户端。
func vmExportClient() *http.Client {
	return &http.Client{Timeout: 6 * time.Hour}
}

// createVMBackup 把 VictoriaMetrics 最近 N 天的原生格式数据导出成一个 .native.gz。
func (s *Server) createVMBackup(operator, note string) (BackupMeta, error) {
	c := s.cfg.VMConfig()
	base := strings.TrimSpace(c.URL)
	if base == "" {
		base = strings.TrimSpace(os.Getenv("AIOPS_VM_URL"))
	}
	if base == "" {
		return BackupMeta{}, fmt.Errorf("未配置 VictoriaMetrics 地址，无法备份时序数据")
	}
	cfg := s.cfg.BackupCfg()
	days := cfg.VMDays
	if days <= 0 {
		days = 90
	}
	dir := backupDir(cfg)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return BackupMeta{}, err
	}
	end := time.Now()
	start := end.AddDate(0, 0, -days)
	u := strings.TrimRight(base, "/") + "/api/v1/export/native" +
		fmt.Sprintf("?match[]=%s&start=%d&end=%d", "%7B__name__%21%3D%22%22%7D", start.Unix(), end.Unix())

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return BackupMeta{}, err
	}
	resp, err := vmExportClient().Do(req)
	if err != nil {
		return BackupMeta{}, fmt.Errorf("导出 VictoriaMetrics 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return BackupMeta{}, fmt.Errorf("导出 VictoriaMetrics 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	id := fmt.Sprintf("%s%s.native.gz", backupPrefixVM, end.Format("20060102-150405"))
	path := filepath.Join(dir, id)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return BackupMeta{}, err
	}
	gz := gzip.NewWriter(f)
	if _, err := io.Copy(gz, resp.Body); err != nil {
		gz.Close()
		f.Close()
		os.Remove(path) // 半截文件比没有更危险：它会以"有备份"的样子躺在列表里
		return BackupMeta{}, fmt.Errorf("写入时序备份失败: %w", err)
	}
	if err := gz.Close(); err != nil {
		f.Close()
		os.Remove(path)
		return BackupMeta{}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return BackupMeta{}, err
	}
	return s.finishBackupArtifact(id, path, operator,
		strings.TrimSpace(fmt.Sprintf("%s vm_days=%d", note, days)))
}

// createRecordingsBackup 打包终端与桌面录像目录。录像是审计取证的最后一环，
// 只备份 PG 等于把"谁在生产上敲了什么"这段证据留在了单块磁盘上。
func (s *Server) createRecordingsBackup(operator, note string) (BackupMeta, error) {
	cfg := s.cfg.BackupCfg()
	dir := backupDir(cfg)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return BackupMeta{}, err
	}
	srcs := map[string]string{}
	if s.term != nil && strings.TrimSpace(s.term.recDir) != "" {
		srcs["terminal"] = s.term.recDir
	}
	if s.desk != nil && strings.TrimSpace(s.desk.recDir) != "" {
		srcs["desktop"] = s.desk.recDir
	}
	if len(srcs) == 0 {
		return BackupMeta{}, fmt.Errorf("未配置录像目录，无需备份")
	}

	id := fmt.Sprintf("%s%s.tar.gz", backupPrefixRec, time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, id)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return BackupMeta{}, err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	count := 0
	for kind, src := range srcs {
		if _, err := os.Stat(src); err != nil {
			continue // 目录还没建（从未开过终端）不算错误
		}
		err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() || !info.Mode().IsRegular() {
				return nil // 跳过无法读取的条目，不让一份坏文件毁掉整包
			}
			rel, rerr := filepath.Rel(src, p)
			if rerr != nil {
				return nil
			}
			// Open BEFORE WriteHeader. A header with Size=N followed by a skipped
			// body (Open failed after Stat — race with rotation/delete) shifts every
			// later entry by N bytes and produces a "successful" corrupt archive.
			fh, oerr := os.Open(p)
			if oerr != nil {
				return nil
			}
			st, serr := fh.Stat()
			if serr != nil || st == nil || !st.Mode().IsRegular() {
				fh.Close()
				return nil
			}
			hdr, herr := tar.FileInfoHeader(st, "")
			if herr != nil {
				fh.Close()
				return nil
			}
			hdr.Name = filepath.ToSlash(filepath.Join(kind, rel))
			if err := tw.WriteHeader(hdr); err != nil {
				fh.Close()
				return err
			}
			_, copyErr := io.Copy(tw, fh)
			fh.Close()
			if copyErr != nil {
				return copyErr
			}
			count++
			return nil
		})
		if err != nil {
			tw.Close()
			gz.Close()
			f.Close()
			os.Remove(path)
			return BackupMeta{}, fmt.Errorf("打包录像失败: %w", err)
		}
	}
	if err := tw.Close(); err != nil {
		gz.Close()
		f.Close()
		os.Remove(path)
		return BackupMeta{}, err
	}
	if err := gz.Close(); err != nil {
		f.Close()
		os.Remove(path)
		return BackupMeta{}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return BackupMeta{}, err
	}
	return s.finishBackupArtifact(id, path, operator,
		strings.TrimSpace(fmt.Sprintf("%s files=%d", note, count)))
}

// finishBackupArtifact 走与 PG 备份一致的收尾：校验和 → 台账 → 远端上传 → 按种类保留。
func (s *Server) finishBackupArtifact(id, path, operator, note string) (BackupMeta, error) {
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
	cfg := s.cfg.BackupCfg()
	if cfg.Remote.Enabled {
		if err := s.uploadBackupRemote(path, id); err != nil {
			slog.Error("远端备份上传失败", "err", err, "id", id)
			meta.Note = strings.TrimSpace(meta.Note + " remote_upload_error:" + err.Error())
		} else {
			meta.Note = strings.TrimSpace(meta.Note + " remote_ok")
		}
	}
	s.pruneBackups(cfg.RetainCount)
	return meta, nil
}

// createFullBackup 做一次"整套"备份：PG + 时序 + 录像。任何一部分失败都不影响其它
// 部分继续——半份备份也远好过没有，但失败必须原样报出去，不能吞掉。
func (s *Server) createFullBackup(operator, note string) ([]BackupMeta, map[string]string) {
	var metas []BackupMeta
	errs := map[string]string{}
	if m, err := s.createPGBackup(operator, note); err != nil {
		errs["postgres"] = err.Error()
	} else {
		metas = append(metas, m)
	}
	if m, err := s.createVMBackup(operator, note); err != nil {
		errs["victoriametrics"] = err.Error()
	} else {
		metas = append(metas, m)
	}
	if m, err := s.createRecordingsBackup(operator, note); err != nil {
		errs["recordings"] = err.Error()
	} else {
		metas = append(metas, m)
	}
	return metas, errs
}

// handleCreateFullBackup POST /api/v1/admin/backups/full
func (s *Server) handleCreateFullBackup(w http.ResponseWriter, r *http.Request) {
	metas, errs := s.createFullBackup(s.actorName(r), "manual-full")
	ids := make([]string, 0, len(metas))
	for _, m := range metas {
		ids = append(ids, m.ID)
	}
	s.addAuditLog(r, LogEntry{Kind: KindOperation, Level: "info",
		Message: Tz("log.backup_full", strings.Join(ids, ", "), len(errs))})
	code := http.StatusOK
	if len(metas) == 0 {
		code = http.StatusBadRequest
	}
	writeJSON(w, code, map[string]any{
		"ok":      len(errs) == 0,
		"backups": metas,
		"errors":  errs,
		"hint":    Tz("backup.full_hint"),
	})
}
