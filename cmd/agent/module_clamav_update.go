package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ClamAV signature refresh.
//
// The freshness check in the host security scan only reports that a database is
// stale; this module is the fix. It exists because freshclam is the one piece
// of the update chain that ignores HTTP_PROXY/HTTPS_PROXY — the proxy has to go
// into a config file — which is exactly why signature updates silently rot on
// hosts behind an egress proxy.

const (
	freshclamDefaultTimeout = 15 * time.Minute
	freshclamMaxTimeout     = 60 * time.Minute
)

func findFreshclamBin() string {
	if p, err := exec.LookPath("freshclam"); err == nil && p != "" {
		return p
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		if pref := strings.TrimSpace(cmdOut(4, "brew", "--prefix", "clamav")); pref != "" && !strings.Contains(strings.ToLower(pref), "error") {
			candidates = append(candidates,
				filepath.Join(pref, "bin", "freshclam"),
				filepath.Join(pref, "sbin", "freshclam"))
		}
		candidates = append(candidates, "/opt/homebrew/bin/freshclam", "/usr/local/bin/freshclam")
	case "windows":
		candidates = append(candidates, `C:\Program Files\ClamAV\freshclam.exe`, `C:\ClamAV\freshclam.exe`)
	default:
		candidates = append(candidates, "/usr/bin/freshclam", "/usr/local/bin/freshclam", "/bin/freshclam")
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// freshclamConfigPaths lists where the stock config lives per platform.
func freshclamConfigPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/opt/homebrew/etc/clamav/freshclam.conf", "/usr/local/etc/clamav/freshclam.conf"}
	case "windows":
		return []string{`C:\Program Files\ClamAV\freshclam.conf`, `C:\ClamAV\freshclam.conf`}
	default:
		return []string{"/etc/clamav/freshclam.conf", "/usr/local/etc/freshclam.conf", "/etc/freshclam.conf"}
	}
}

func findFreshclamConfig() string {
	for _, p := range freshclamConfigPaths() {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// parseProxyHostPort splits an operator-supplied proxy into the host and port
// freshclam.conf wants. It accepts the usual URL forms because that is what
// people paste, but freshclam takes them separately.
func parseProxyHostPort(raw string) (host string, port int, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, nil
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme := strings.ToLower(raw[:i])
		if scheme != "http" && scheme != "https" {
			return "", 0, fmt.Errorf("freshclam 仅支持 http/https 代理，收到 %s", scheme)
		}
		raw = raw[i+3:]
	}
	raw = strings.TrimSuffix(raw, "/")
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		// Credentials belong in HTTPProxyUsername/Password, which we do not
		// write: putting a password into a generated file on a managed host is
		// not something to do implicitly.
		return "", 0, fmt.Errorf("代理地址不要包含账号密码，请在主机 freshclam.conf 中单独配置")
	}
	host = raw
	port = 8080
	if i := strings.LastIndex(raw, ":"); i > 0 && !strings.Contains(raw[i+1:], "]") {
		p, convErr := strconv.Atoi(raw[i+1:])
		if convErr != nil || p <= 0 || p > 65535 {
			return "", 0, fmt.Errorf("代理端口无效：%s", raw[i+1:])
		}
		host, port = raw[:i], p
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return "", 0, fmt.Errorf("代理地址缺少主机名")
	}
	for _, c := range host {
		// Keep the value shell-free and config-safe; it is written verbatim.
		if c < '!' || c > '~' || c == '"' || c == '\'' || c == '\n' {
			return "", 0, fmt.Errorf("代理地址包含非法字符")
		}
	}
	return host, port, nil
}

// buildFreshclamConfig derives a run-scoped config from the host's own file,
// replacing any existing proxy directives. The system config is never edited:
// a failed update must not leave the host's own freshclam broken.
func buildFreshclamConfig(base string, proxyHost string, proxyPort int) string {
	var out []string
	for _, ln := range strings.Split(base, "\n") {
		trimmed := strings.TrimSpace(ln)
		low := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(low, "httpproxyserver"), strings.HasPrefix(low, "httpproxyport"):
			continue
		}
		out = append(out, strings.TrimRight(ln, "\r"))
	}
	body := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if body != "" {
		body += "\n"
	}
	body += fmt.Sprintf("HTTPProxyServer %s\nHTTPProxyPort %d\n", proxyHost, proxyPort)
	return body
}

// moduleClamavUpdate refreshes the ClamAV signature database.
//
// Args:
//
//	proxy       optional http proxy (host:port or http://host:port)
//	timeout_sec optional, clamped to [60, 3600]
//
// ctx 是会话级取消信号：freshclam 是所有模块里跑得最久的一个（默认 15 分钟，可放到 60），
// 少了它，「停止剧本」之后这台机器还会继续拉整套病毒库。
func moduleClamavUpdate(ctx context.Context, args map[string]string) ([]byte, int) {
	ctx = moduleCtx(ctx)
	// Validate arguments before touching the host: a malformed proxy is the
	// caller's mistake and should read the same on every machine, whether or
	// not ClamAV happens to be installed there.
	proxyHost, proxyPort, err := parseProxyHostPort(args["proxy"])
	if err != nil {
		return []byte(err.Error()), 1
	}

	bin := findFreshclamBin()
	if bin == "" {
		return []byte("未找到 freshclam。" + clamInstallSuggest()), 1
	}

	timeout := freshclamDefaultTimeout
	if v := strings.TrimSpace(args["timeout_sec"]); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(clampSec(n, 60, int(freshclamMaxTimeout/time.Second))) * time.Second
		}
	}

	argv := []string{bin}
	if proxyHost != "" {
		base := ""
		if cfgPath := findFreshclamConfig(); cfgPath != "" {
			if raw, rerr := os.ReadFile(cfgPath); rerr == nil {
				base = string(raw)
			}
		}
		if strings.TrimSpace(base) == "" {
			// freshclam refuses to start on an empty config, and the stock file
			// ships commented-out; supply the minimum it needs.
			base = "DatabaseMirror database.clamav.net\n"
		}
		tmp, terr := os.CreateTemp("", "aiops-freshclam-*.conf")
		if terr != nil {
			return []byte("无法创建临时配置：" + terr.Error()), 1
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		_, werr := tmp.WriteString(buildFreshclamConfig(base, proxyHost, proxyPort))
		_ = tmp.Close()
		if werr != nil {
			return []byte("无法写入临时配置：" + werr.Error()), 1
		}
		argv = append(argv, "--config-file="+tmpName)
	}

	before := clamavDBFreshness()

	if moduleStopped(ctx) {
		return []byte("clamav_update " + moduleStopMsg), moduleStopExit
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.WaitDelay = 5 * time.Second
	raw, runErr := moduleCombinedOutput(cmd)
	out := strings.TrimSpace(string(raw))

	var b strings.Builder
	fmt.Fprintf(&b, "$ %s\n", strings.Join(argv, " "))
	if proxyHost != "" {
		fmt.Fprintf(&b, "proxy=%s:%d\n", proxyHost, proxyPort)
	}
	if out != "" {
		b.WriteString(truncateStr(out, 4000))
		b.WriteString("\n")
	}

	after := clamavDBFreshness()
	if !after.IsZero() {
		fmt.Fprintf(&b, "db_updated=%s db_age_days=%d\n",
			after.Format("2006-01-02 15:04:05"), int(time.Since(after).Hours()/24))
	}

	// 先分辨「运维按了停止」和「freshclam 自己跑超时」：后者要提示改 proxy/timeout_sec，
	// 前者什么都不用改，把这句提示贴给一次主动停止只会误导人。
	if moduleStopped(ctx) {
		b.WriteString("更新已中止" + moduleStopMsg + "\n")
		return []byte(b.String()), moduleStopExit
	}
	if cctx.Err() != nil {
		b.WriteString("更新超时。若主机需要经代理出网，请在参数中填写 proxy，或调大 timeout_sec。\n")
		return []byte(b.String()), 1
	}
	if runErr != nil {
		// freshclam exits non-zero when the database is already current on some
		// builds; treat a newer database as success regardless of exit code.
		if !after.IsZero() && after.After(before) {
			b.WriteString("病毒库已更新（freshclam 返回非零退出码，但数据库时间已刷新）。\n")
			return []byte(b.String()), 0
		}
		b.WriteString("更新失败：" + runErr.Error() + "\n")
		if strings.Contains(strings.ToLower(out), "permission denied") {
			b.WriteString("提示：freshclam 通常需要以 root 或 clamav 用户运行。\n")
		}
		return []byte(b.String()), 1
	}
	return []byte(b.String()), 0
}

func clampSec(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
