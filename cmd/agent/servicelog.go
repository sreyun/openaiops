package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"aiops-monitor/shared"
)

// Agent runtime log retention (install directory):
//   - active file + rotated backups = serviceLogMaxFiles
//   - each file capped at serviceLogMaxBytes
//
// Rolling overwrite keeps disk use bounded (~70 MiB worst case).
const (
	serviceLogMaxBytes = 10 << 20 // 10 MiB per file
	serviceLogMaxFiles = 7        // agent.log + agent.log.1 .. agent.log.6
)

// startServiceFileLog mirrors slog output into <dir>/<name> with size-based
// rotation. Enabled on every platform: Windows services / hidden consoles have
// no usable stderr, and Linux/macOS operators still need a stable on-disk trail
// under the install directory (journald/launchd alone is easy to miss).
func startServiceFileLog(dir, name string) {
	if dir == "" || name == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, name)
	w := newRotatingFile(path, serviceLogMaxBytes, serviceLogMaxFiles)
	if w == nil {
		return
	}
	slog.SetDefault(slog.New(newAgentTextHandler(io.MultiWriter(shared.NewConsoleAwareWriter(os.Stderr), w))))
	slog.Info("运行日志已写入安装目录（滚动覆盖）",
		"path", path,
		"max_file_mb", serviceLogMaxBytes>>20,
		"max_files", serviceLogMaxFiles)
}

// rotatingFile is a minimal size-capped writer. Log volume here is a handful of
// lines per report cycle, so a mutex around a plain file is plenty and avoids
// pulling in a logging dependency for what is purely a diagnostics aid.
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int // including the active file; backups are path.1 .. path.(maxFiles-1)
	f        *os.File
	n        int64
}

func newRotatingFile(path string, maxBytes int64, maxFiles int) *rotatingFile {
	if maxBytes < 1 {
		maxBytes = serviceLogMaxBytes
	}
	if maxFiles < 1 {
		maxFiles = 1
	}
	r := &rotatingFile{path: path, maxBytes: maxBytes, maxFiles: maxFiles}
	if err := r.open(); err != nil {
		return nil
	}
	return r
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	size := int64(0)
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}
	// slog writes UTF-8. Notepad on Chinese Windows Server assumes the ANSI code
	// page (GBK) for a BOM-less file and renders every log line as mojibake —
	// unhelpful when this file exists precisely to be read during an incident.
	if size == 0 {
		if n, err := f.Write([]byte{0xEF, 0xBB, 0xBF}); err == nil {
			size = int64(n)
		}
	}
	r.f, r.n = f, size
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return len(p), nil // degraded: never fail a log write into a service crash
	}
	if r.n+int64(len(p)) > r.maxBytes {
		r.rotateLocked()
	}
	n, _ := r.f.Write(p)
	r.n += int64(n)
	// Never surface an error: this writer is half of an io.MultiWriter, and
	// failing here (disk full, ACL change) would also stop the stderr copy that
	// systemd/launchd rely on. Losing a diagnostics line beats losing logging.
	return len(p), nil
}

func (r *rotatingFile) rotateLocked() {
	if r.f != nil {
		_ = r.f.Close()
		r.f = nil
	}
	// Drop the oldest backup, then shift .N-1 → .N … .1 → .2, active → .1.
	if r.maxFiles > 1 {
		oldest := r.backupPath(r.maxFiles - 1)
		_ = os.Remove(oldest)
		for i := r.maxFiles - 2; i >= 1; i-- {
			from, to := r.backupPath(i), r.backupPath(i+1)
			_ = os.Rename(from, to)
		}
		_ = os.Rename(r.path, r.backupPath(1))
	} else {
		_ = os.Remove(r.path)
	}
	if err := r.open(); err != nil {
		r.f = nil
	}
}

func (r *rotatingFile) backupPath(i int) string {
	return r.path + "." + strconv.Itoa(i)
}

// Close implements io.Closer for tests; production leaves the FD open for the
// process lifetime.
func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// rotatingFileNames lists the active log plus backup suffixes for uninstall /
// docs (agent.log, agent.log.1, …).
func rotatingFileNames(base string, maxFiles int) []string {
	if maxFiles < 1 {
		maxFiles = 1
	}
	out := make([]string, 0, maxFiles)
	out = append(out, base)
	for i := 1; i < maxFiles; i++ {
		out = append(out, fmt.Sprintf("%s.%d", base, i))
	}
	return out
}
