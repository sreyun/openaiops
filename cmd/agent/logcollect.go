package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aiops-monitor/shared"
)

// Log collection — agent side.
//
// The agent tails the configured log paths (--log-paths / config log_paths). Each
// entry may be a FILE or a DIRECTORY: directories are expanded to the log files
// beneath them (.log/.out/.err/.txt and rotated *.log.*), rescanned periodically so
// newly-created files are picked up. Only lines appended AFTER a file is first seen
// are forwarded (we seek to EOF), so enabling collection never floods historical
// logs. Rotation/truncation is detected (size < last offset) and re-read from the top.
//
// Batches are gzip-compressed + AES-256-GCM encrypted before upload whenever the
// server handed us a per-agent log key at registration (default on); set
// --log-encrypt=false to send plaintext for debugging.

var logCollectHTTP = &http.Client{Timeout: 20 * time.Second}

func (a *Agent) runLogCollectorFor(t *serverTarget) {
	if len(a.logPaths) == 0 || a.identity.Fingerprint == "" {
		return
	}
	slog.Info("日志采集已启用", "server", t.server, "配置路径数", len(a.logPaths), "加密", a.logEncrypt && len(t.logKey) == 32)
	offsets := make(map[string]int64)
	ensure := func() []string { // 展开目标文件；新文件定位到当前末尾（只采后续新行）
		targets := expandLogTargets(a.logPaths)
		for _, p := range targets {
			if _, ok := offsets[p]; !ok {
				if fi, err := os.Stat(p); err == nil {
					offsets[p] = fi.Size()
				} else {
					offsets[p] = 0
				}
			}
		}
		return targets
	}
	targets := ensure()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	cycle := 0
	for range ticker.C {
		cycle++
		if cycle%6 == 0 { // ~60s 重新扫描目录，纳入新出现的日志文件
			targets = ensure()
		}
		var batch []shared.LogLine
		now := time.Now().Unix()
		for _, p := range targets {
			lines, newOff := readNewLines(p, offsets[p])
			offsets[p] = newOff
			for _, ln := range lines {
				batch = append(batch, shared.LogLine{Ts: now, Source: p, Level: classifyLogLevel(ln), Message: ln})
			}
			if len(batch) >= 500 {
				break
			}
		}
		if len(batch) > 500 {
			batch = batch[len(batch)-500:] // keep the most recent
		}
		if len(batch) > 0 {
			a.sendLogBatch(t, batch)
		}
	}
}

// expandLogTargets 把用户配置的路径展开为要 tail 的具体文件：文件→直接采集；
// 目录→采集其下的日志文件（.log/.out/.err/.txt 及含 .log 的轮转文件）。
func expandLogTargets(paths []string) []string {
	var files []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !fi.IsDir() {
			add(p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && isLogFileName(e.Name()) {
				add(filepath.Join(p, e.Name()))
			}
		}
	}
	return files
}

func isLogFileName(name string) bool {
	l := strings.ToLower(name)
	for _, suf := range []string{".log", ".out", ".err", ".txt"} {
		if strings.HasSuffix(l, suf) {
			return true
		}
	}
	return strings.Contains(l, ".log") // 轮转文件如 access.log.1 / error.log.2024-01-01
}

// readNewLines returns the lines appended after `off` plus the new offset.
// A file smaller than `off` is treated as rotated/truncated and re-read from 0;
// a very large gap only tails the last ~2MB to bound a single cycle.
func readNewLines(path string, off int64) ([]string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, off
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, off
	}
	size := fi.Size()
	if size < off {
		off = 0
	}
	if size <= off {
		return nil, size
	}
	start := off
	const maxRead = 2 << 20
	if size-start > maxRead {
		start = size - maxRead
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, size
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		ln := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	return lines, size
}

func classifyLogLevel(line string) string {
	u := strings.ToUpper(line)
	switch {
	case strings.Contains(u, "ERROR") || strings.Contains(u, "FATAL") || strings.Contains(u, "PANIC") || strings.Contains(u, "CRITICAL") || strings.Contains(u, "[ERR"):
		return "error"
	case strings.Contains(u, "WARN"):
		return "warn"
	case strings.Contains(u, "DEBUG") || strings.Contains(u, "TRACE"):
		return "debug"
	default:
		return "info"
	}
}

// sealLogAgent gzip 压缩后 AES-256-GCM 加密（nonce 前置），与服务端 openLog 对应。
func sealLogAgent(key, plaintext []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(plaintext); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, buf.Bytes(), nil), nil
}

func (a *Agent) sendLogBatch(t *serverTarget, lines []shared.LogLine) {
	body, _ := json.Marshal(shared.LogBatch{HostID: t.hostIDOr(a.identity.HostID), Lines: lines})
	enc := ""
	if a.logEncrypt && len(t.logKey) == 32 { // 默认加密上报（服务端下发密钥时）
		if sealed, err := sealLogAgent(t.logKey, body); err == nil {
			body, enc = sealed, "aesgcm-gzip"
		}
	}
	req, err := http.NewRequest(http.MethodPost, t.server+"/api/v1/agent/logs", bytes.NewReader(body))
	if err != nil {
		return
	}
	if enc != "" {
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Log-Enc", enc)
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Agent-Fingerprint", a.identity.Fingerprint)
	if resp, err := logCollectHTTP.Do(req); err == nil {
		resp.Body.Close()
	}
}
