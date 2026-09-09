package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// runSelfTest verifies that this machine can actually become a monitored host,
// and explains precisely what is broken when it cannot.
//
// The installer used to print "done. Check the dashboard for this host." after
// merely observing that the Windows service reached the Running state. A service
// whose token was rejected, whose DNS does not resolve, or whose outbound traffic
// is dropped by a firewall reaches Running just fine and then reports into the
// void — the operator saw a fully green install and an empty dashboard, with no
// console and no log to look at. This walks the same path the agent walks
// (resolve → connect → TLS → POST /api/v1/agent/register) and maps each failure
// to the thing an operator has to change.
//
// Registration is the right probe rather than a report: the server creates the
// host record with LastSeen set at register time, so a passing self-test means
// the host card exists. It is also idempotent — the service registering with the
// same host_id and fingerprint a second later changes nothing and consumes no
// extra install-token use.
func runSelfTest(out io.Writer, servers []ServerConfig, hostID, cfgPath, stateFile string) int {
	id := identityForSelfTest(hostID)
	fmt.Fprintf(out, "[selftest] 配置文件 config : %s\n", cfgPath)
	fmt.Fprintf(out, "[selftest] 主机 host_id    : %s\n", id.HostID)
	fmt.Fprintf(out, "[selftest] 主机名 hostname : %s\n", id.Hostname)
	fmt.Fprintf(out, "[selftest] 机器指纹 fp     : %s\n", id.Fingerprint)
	fmt.Fprintf(out, "[selftest] Agent 版本      : %s\n", id.AgentVersion)

	if id.Fingerprint == "" {
		fmt.Fprintln(out, "[selftest] FAIL 无法读取机器指纹（machine-id 与主网卡 MAC 都取不到）")
		fmt.Fprintln(out, "[selftest]      服务端会拒绝没有指纹的注册。请确认注册表 HKLM\\SOFTWARE\\Microsoft\\Cryptography\\MachineGuid 可读，")
		fmt.Fprintln(out, "[selftest]      或设置环境变量 AIOPS_MACHINE_ID 为一个稳定值后重试。")
		return 1
	}

	failed := 0
	for _, sc := range servers {
		canonical, err := selfTestTarget(out, sc, id, cfgPath)
		if err != nil {
			failed++
			continue
		}
		// Server may return the previously-known host_id (reinstall). Persist it so
		// the service and selftest share one identity — otherwise the dashboard
		// briefly shows a ghost host that the service never reports under.
		if canonical != "" && canonical != id.HostID && stateFile != "" {
			fmt.Fprintf(out, "[selftest] 同步身份文件 host_id %s → %s\n", id.HostID, canonical)
			persistHostID(stateFile, canonical, id.Fingerprint)
			id.HostID = canonical
		}
	}
	if failed > 0 {
		fmt.Fprintf(out, "[selftest] FAIL %d/%d 个服务端不可用，主机不会出现在面板。\n", failed, len(servers))
		return 1
	}
	fmt.Fprintln(out, "[selftest] PASS 注册成功，主机已可在面板中看到（首批指标最多 30 秒后到达）。")
	return 0
}

// selfTestIdentity is the subset of the report identity the handshake needs.
type selfTestIdentity struct {
	HostID       string
	Hostname     string
	Fingerprint  string
	AgentVersion string
}

func identityForSelfTest(hostID string) selfTestIdentity {
	return selfTestIdentity{
		HostID:       hostID,
		Hostname:     hostname(),
		Fingerprint:  machineFingerprint(),
		AgentVersion: agentVersion(),
	}
}

func selfTestTarget(out io.Writer, sc ServerConfig, id selfTestIdentity, cfgPath string) (canonical string, err error) {
	server := strings.TrimRight(strings.TrimSpace(sc.Server), "/")
	fmt.Fprintf(out, "[selftest] --- 目标服务端 %s ---\n", server)

	u, err := url.Parse(server)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		fmt.Fprintf(out, "[selftest] FAIL 服务端地址非法: %q（应形如 https://panel.example.com）\n", server)
		return "", errors.New("bad url")
	}
	if strings.Contains(u.Hostname(), "localhost") || u.Hostname() == "127.0.0.1" {
		fmt.Fprintln(out, "[selftest] WARN 上报地址是本机回环地址，远程主机永远连不上；请在面板重新生成安装命令。")
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}

	// DNS. Reported separately because "name does not resolve" and "packets are
	// dropped" need completely different people to fix them.
	if net.ParseIP(host) == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		cancel()
		if err != nil {
			fmt.Fprintf(out, "[selftest] FAIL DNS 解析 %s 失败: %v\n", host, err)
			fmt.Fprintln(out, "[selftest]      请检查本机 DNS 设置，或在 config.yaml 里把 server 改成 IP 地址。")
			return "", err
		}
		fmt.Fprintf(out, "[selftest] OK   DNS 解析 %s → %s\n", host, strings.Join(addrs, ", "))
	}

	// TCP.
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 8*time.Second)
	if err != nil {
		fmt.Fprintf(out, "[selftest] FAIL TCP 连接 %s:%s 失败: %v\n", host, port, err)
		if isTimeout(err) {
			fmt.Fprintln(out, "[selftest]      连接超时 = 数据包被丢弃，通常是本机防火墙、云安全组或出口策略拦截了该端口。")
		} else {
			fmt.Fprintln(out, "[selftest]      连接被拒绝 = 服务端未监听该端口，或地址/端口填错。")
		}
		return "", err
	}
	_ = conn.Close()
	fmt.Fprintf(out, "[selftest] OK   TCP 连接 %s:%s (%dms)\n", host, port, time.Since(start).Milliseconds())

	// Register.
	body, _ := json.Marshal(map[string]string{
		"host_id":     id.HostID,
		"hostname":    id.Hostname,
		"token":       sc.Token,
		"fingerprint": id.Fingerprint,
	})
	client := newAgentHTTPClient(20 * time.Second)
	resp, err := client.Post(server+"/api/v1/agent/register", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(out, "[selftest] FAIL 注册请求失败: %v\n", err)
		var certErr x509.UnknownAuthorityError
		var hostErr x509.HostnameError
		switch {
		case errors.As(err, &certErr), errors.As(err, &hostErr), strings.Contains(err.Error(), "x509"):
			fmt.Fprintln(out, "[selftest]      TLS 证书不受信任。自签名证书请在 config.yaml 配置 ca_cert，或临时设置 tls_skip_verify: true。")
		case isTimeout(err):
			fmt.Fprintln(out, "[selftest]      TCP 通了但 HTTP 无响应，通常是中间代理/WAF 拦截，或服务端过载。")
		}
		return "", err
	}
	defer resp.Body.Close()

	finalURL := ""
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = strings.TrimRight(resp.Request.URL.Scheme+"://"+resp.Request.URL.Host, "/")
	}
	if finalURL != "" && finalURL != server {
		fmt.Fprintf(out, "[selftest] INFO 注册经重定向：%s → %s（已保留 POST 握手）\n", server, finalURL)
		upgradeConfigServerURL(cfgPath, server, finalURL, out)
	}

	switch {
	case resp.StatusCode < 300:
		var r struct {
			HostID string `json:"host_id"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&r)
		if r.HostID != "" && r.HostID != id.HostID {
			fmt.Fprintf(out, "[selftest] OK   注册成功，服务端沿用既有身份 host_id=%s（重装保留历史数据）\n", r.HostID)
			return r.HostID, nil
		}
		fmt.Fprintf(out, "[selftest] OK   注册成功 host_id=%s\n", id.HostID)
		return id.HostID, nil
	case resp.StatusCode == http.StatusForbidden:
		fmt.Fprintln(out, "[selftest] FAIL 注册被拒绝 (403)：安装 Token 无效、已过期，或使用次数已用完。")
		fmt.Fprintln(out, "[selftest]      请在面板「安装客户端」页面重新生成安装命令后重装。")
		return "", errors.New("token rejected")
	case resp.StatusCode == http.StatusConflict:
		fmt.Fprintln(out, "[selftest] FAIL 注册冲突 (409)：该 host_id 已被另一台机器占用。")
		fmt.Fprintln(out, "[selftest]      多半是克隆虚拟机时带上了 agent_state.json。删除该文件后重启 Agent 即可重新分配身份。")
		return "", errors.New("host id conflict")
	case resp.StatusCode == http.StatusNotFound:
		fmt.Fprintln(out, "[selftest] FAIL 注册返回 HTTP 404。")
		fmt.Fprintln(out, "[selftest]      常见原因：config.yaml 里 server 仍是 http://，反代 301 到 https 后旧版 Agent 会把 POST 改成 GET，")
		fmt.Fprintln(out, "[selftest]      服务端只有 POST /api/v1/agent/register，于是变成 404。请把 server 改成 https:// 后重试，或升级 Agent。")
		snip := readHTTPBodySnip(resp.Body, 180)
		if snip != "" {
			fmt.Fprintf(out, "[selftest]      响应摘要: %s\n", snip)
		}
		return "", fmt.Errorf("http 404")
	default:
		fmt.Fprintf(out, "[selftest] FAIL 注册返回 HTTP %d，服务端拒绝了本次握手。\n", resp.StatusCode)
		snip := readHTTPBodySnip(resp.Body, 180)
		if snip != "" {
			fmt.Fprintf(out, "[selftest]      响应摘要: %s\n", snip)
		}
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
}

func readHTTPBodySnip(r io.Reader, n int) string {
	if n <= 0 {
		n = 120
	}
	b, _ := io.ReadAll(io.LimitReader(r, int64(n)))
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// upgradeConfigServerURL rewrites http://… → https://… in config after a successful
// redirect so subsequent service reports skip the extra hop.
func upgradeConfigServerURL(cfgPath, from, to string, out io.Writer) {
	cfgPath = strings.TrimSpace(cfgPath)
	from = strings.TrimRight(strings.TrimSpace(from), "/")
	to = strings.TrimRight(strings.TrimSpace(to), "/")
	if cfgPath == "" || from == "" || to == "" || from == to {
		return
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil || !bytes.Contains(b, []byte(from)) {
		return
	}
	nb := bytes.ReplaceAll(b, []byte(from), []byte(to))
	if bytes.Equal(b, nb) {
		return
	}
	// Atomic replace: os.WriteFile opens O_TRUNC then writes — a crash/SIGKILL
	// between those steps leaves an empty config.yaml. The next start then
	// falls back to localhost:8529 while the service looks healthy, and the
	// host stays offline forever. Same pattern as persistHostID.
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, nb, 0o644); err != nil {
		fmt.Fprintf(out, "[selftest] WARN 未能自动把 config 中的 server 改成 %s: %v\n", to, err)
		return
	}
	if err := os.Rename(tmp, cfgPath); err != nil {
		_ = os.Remove(tmp)
		// Windows cannot always rename over an existing target; fall back but
		// never truncate-first into the live path on the primary attempt.
		if err2 := os.WriteFile(cfgPath, nb, 0o644); err2 != nil {
			fmt.Fprintf(out, "[selftest] WARN 未能自动把 config 中的 server 改成 %s: %v\n", to, err2)
			return
		}
	}
	fmt.Fprintf(out, "[selftest] INFO 已将 config 中的 server 更新为 %s（避免每次 http→https 跳转）\n", to)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
