package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// —— 只读运维模块：禁止改配置/启停服务/写文件。异常时返回可读错误与非零退出码。——

func moduleDiskUsage(ctx context.Context) ([]byte, int) {
	switch runtime.GOOS {
	case "windows":
		return winCIMDiskUsageText(ctx)
	case "darwin":
		return runModuleCmds(ctx, [][]string{{"df", "-hP"}})
	default:
		return runModuleCmds(ctx, [][]string{{"df", "-hT"}})
	}
}

func moduleMemInfo(ctx context.Context) ([]byte, int) {
	var b strings.Builder
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Fprintf(&b, "go_alloc_mb=%.1f\n", float64(ms.Alloc)/1024/1024)
	fmt.Fprintf(&b, "go_sys_mb=%.1f\n", float64(ms.Sys)/1024/1024)
	switch runtime.GOOS {
	case "linux":
		if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
			b.WriteString("--- /proc/meminfo (head) ---\n")
			lines := strings.Split(string(raw), "\n")
			for i, ln := range lines {
				if i >= 12 {
					break
				}
				b.WriteString(ln)
				b.WriteByte('\n')
			}
			return []byte(b.String()), 0
		}
	case "darwin":
		out, exit := runModuleCmds(ctx, [][]string{{"vm_stat"}, {"sysctl", "-n", "hw.memsize"}})
		b.Write(out)
		return []byte(b.String()), exit
	case "windows":
		out, exit := winCIMMemInfoText(ctx)
		b.Write(out)
		return []byte(b.String()), exit
	}
	return []byte(b.String()), 0
}

func moduleCPULoad(ctx context.Context) ([]byte, int) {
	var b strings.Builder
	fmt.Fprintf(&b, "cpus=%d\ngoos=%s\ngoarch=%s\n", runtime.NumCPU(), runtime.GOOS, runtime.GOARCH)
	switch runtime.GOOS {
	case "linux":
		if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
			fmt.Fprintf(&b, "loadavg=%s", string(raw))
		}
		if raw, err := os.ReadFile("/proc/stat"); err == nil {
			lines := strings.Split(string(raw), "\n")
			if len(lines) > 0 {
				fmt.Fprintf(&b, "stat_cpu=%s\n", lines[0])
			}
		}
		return []byte(b.String()), 0
	case "darwin":
		out, exit := runModuleCmds(ctx, [][]string{{"sysctl", "-n", "vm.loadavg"}, {"sysctl", "-n", "hw.ncpu"}})
		b.Write(out)
		return []byte(b.String()), exit
	case "windows":
		out, exit := winCIMCPULoadText(ctx)
		b.Write(out)
		return []byte(b.String()), exit
	}
	return []byte(b.String()), 0
}

func moduleProcessTop(ctx context.Context) ([]byte, int) {
	switch runtime.GOOS {
	case "windows":
		return runModuleCmds(ctx, [][]string{{"cmd", "/c", "tasklist /FO LIST"}})
	case "darwin":
		return runModuleCmds(ctx, [][]string{{"ps", "-axo", "pid,pcpu,pmem,rss,comm"}})
	default:
		return runModuleCmds(ctx, [][]string{{"ps", "-eo", "pid,pcpu,pmem,rss,comm"}})
	}
}

func moduleUptimeInfo(ctx context.Context) ([]byte, int) {
	switch runtime.GOOS {
	case "windows":
		return runModuleCmds(ctx, [][]string{{"cmd", "/c", "net statistics workstation"}})
	default:
		return runModuleCmds(ctx, [][]string{{"uptime"}, {"who"}})
	}
}

func modulePkgList(ctx context.Context) ([]byte, int) {
	switch runtime.GOOS {
	case "linux":
		switch {
		case have("dpkg"):
			return runModuleCmds(ctx, [][]string{{"dpkg", "-l"}})
		case have("rpm"):
			return runModuleCmds(ctx, [][]string{{"rpm", "-qa"}})
		case have("apk"):
			return runModuleCmds(ctx, [][]string{{"apk", "info"}})
		}
		return []byte("未找到 dpkg/rpm/apk"), 1
	case "darwin":
		if have("brew") {
			return runModuleCmds(ctx, [][]string{{"brew", "list", "--versions"}})
		}
		return []byte("未找到 brew"), 1
	case "windows":
		if have("winget") {
			return runModuleCmds(ctx, [][]string{{"winget", "list"}})
		}
		return winCIMPkgListText(ctx)
	}
	return []byte("不支持的系统"), 1
}

func moduleFileStat(args map[string]string) ([]byte, int) {
	path := strings.TrimSpace(args["path"])
	if path == "" {
		return []byte("file_stat 缺少 path"), 1
	}
	if agentDeniedPath(path) {
		return []byte("拒绝访问敏感路径"), 1
	}
	fi, err := os.Stat(path)
	if err != nil {
		return []byte("stat 失败: " + err.Error()), 1
	}
	abs, _ := filepath.Abs(path)
	var b strings.Builder
	fmt.Fprintf(&b, "path=%s\n", abs)
	fmt.Fprintf(&b, "name=%s\n", fi.Name())
	fmt.Fprintf(&b, "size=%d\n", fi.Size())
	fmt.Fprintf(&b, "mode=%s\n", fi.Mode().String())
	fmt.Fprintf(&b, "isdir=%v\n", fi.IsDir())
	fmt.Fprintf(&b, "mtime=%s\n", fi.ModTime().Format(time.RFC3339))
	return []byte(b.String()), 0
}

func moduleFileHead(args map[string]string) ([]byte, int) {
	path := strings.TrimSpace(args["path"])
	if path == "" {
		return []byte("file_head 缺少 path"), 1
	}
	if agentDeniedPath(path) {
		return []byte("拒绝访问敏感路径"), 1
	}
	n := 64 * 1024
	if v := strings.TrimSpace(args["bytes"]); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 && x <= 256*1024 {
			n = x
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return []byte("打开失败: " + err.Error()), 1
	}
	defer f.Close()
	buf := make([]byte, n)
	nr, _ := f.Read(buf)
	return buf[:nr], 0
}

// agentDeniedPath 是读文件类模块（file_stat / file_head / java_exception_scan）的最后
// 一道闸——服务端 deniedSensitivePath 已经拦过一次，但那一层信任的是"服务端不会被绕过"，
// 这一层不作这个假设。
//
// 清单里除了系统凭据，还必须包含 **Agent 自己的安装目录**：config.yaml 里放着安装
// token 与 relay_secret，agent_state.json 里放着主机身份。少了这一条，一个"只读"巡检
// 就能把整个车队的安装 token 读走——那是能让任意机器注册进面板的凭据，比读 /etc/shadow
// 的现实危害更直接。/proc/*/environ 同理：进程环境变量里全是数据库口令与云 AK。
//
// 匹配前先做路径规范化（反斜杠转正、去 . 与 ..、小写），否则 /etc/./shadow 或
// C:\Windows\..\Windows\System32\config\SAM 这类写法可以一路走过去。
func agentDeniedPath(p string) bool {
	if agentDeniedPathLiteral(p) {
		return true
	}
	// 符号链接：/tmp/x -> /etc/shadow 的写法在字面判定里什么都不像。这道闸是给"以 root
	// 运行的 Agent"设的，而宿主机上的普通用户完全可以自己造一个软链，再等一次只读巡检
	// 顺着它把只有 root 能读的文件读走——典型的 confused deputy。文件不存在时
	// EvalSymlinks 返回错误，那也没什么可读的，按不命中处理。
	if real, err := filepath.EvalSymlinks(strings.TrimSpace(p)); err == nil {
		return agentDeniedPathLiteral(real)
	}
	return false
}

// agentDeniedPathLiteral 按字面（规范化后）判定，不解析软链。
func agentDeniedPathLiteral(p string) bool {
	raw := strings.TrimSpace(p)
	if raw == "" {
		return false
	}
	norm := strings.ToLower(filepath.ToSlash(filepath.Clean(strings.ReplaceAll(raw, "\\", "/"))))
	deny := []string{
		"/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/master.passwd",
		".ssh/", ".gnupg/", ".aws/", ".kube/config",
		"/root/.bash_history",
		"/system32/config/sam", "/system32/config/security",
	}
	for _, d := range deny {
		if strings.Contains(norm, d) {
			return true
		}
	}
	// 私钥与凭据文件（按文件名收口，避免整目录误伤）。
	base := path.Base(norm)
	for _, suf := range []string{".pem", ".key", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", ".kdbx"} {
		if strings.HasSuffix(base, suf) || base == suf {
			return true
		}
	}
	// /proc/<pid>/environ、/proc/self/environ：进程环境变量即凭据。
	if strings.HasPrefix(norm, "/proc/") && strings.HasSuffix(norm, "/environ") {
		return true
	}
	// Agent 自己的安装目录（token / relay_secret / 主机身份）。
	if agentOwnSecretPath(norm) {
		return true
	}
	return false
}

// agentOwnSecretPath 判断路径是否落在 Agent 自己的安装目录里。
// 取当前进程可执行文件所在目录，而不是写死 /opt/aiops-agent —— 安装目录可被
// AIOPS_DIR 改写，非 root 安装还会落在 $HOME/.aiops-agent 下。
func agentOwnSecretPath(norm string) bool {
	for _, f := range []string{"config.yaml", "config.json", "agent_state.json"} {
		if strings.HasSuffix(norm, "/"+f) || norm == f {
			if exe, err := os.Executable(); err == nil {
				dir := strings.ToLower(filepath.ToSlash(filepath.Dir(exe)))
				if strings.HasPrefix(norm, dir+"/") {
					return true
				}
			}
			// 兜底：即使拿不到自身路径，默认安装目录也必须挡住。
			if strings.Contains(norm, "/aiops-agent/") || strings.Contains(norm, "/.aiops-agent/") {
				return true
			}
		}
	}
	return false
}

func moduleServiceStatus(ctx context.Context, args map[string]string) ([]byte, int) {
	name := strings.TrimSpace(args["name"])
	if name == "" {
		return []byte("service_status 缺少 name"), 1
	}
	switch runtime.GOOS {
	case "linux":
		return runModuleCmds(ctx, [][]string{
			{"systemctl", "is-active", name},
			{"systemctl", "status", name, "--no-pager", "-l"},
		})
	case "windows":
		return runModuleCmds(ctx, [][]string{{"sc", "query", name}})
	case "darwin":
		return runModuleCmds(ctx, [][]string{{"brew", "services", "info", name}})
	}
	return []byte("不支持的系统"), 1
}

// moduleJournalLines 把 lines 参数收敛成一个**纯数字且有上限**的字符串。
//
// 两件事都必须做，缺一不可：
//   - 必须是数字：Windows 分支把它拼进 "cmd /c wevtutil …" 的命令行，一个
//     lines="1 & whoami" 就是一次任意命令执行——而 journal_recent 是标注为**只读**的
//     模块，只读巡检（含 AI 闭环）本不该有改动宿主机的能力。这是越权，不只是脏数据。
//   - 必须有上限：journalctl -n 999999999 会把整个 journal（繁忙主机上是 GB 级）读进
//     内存再回传，足以把 1G 内存的小机器上的 Agent 直接撑爆。
func moduleJournalLines(v string) string {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return "80"
	}
	if n > 5000 {
		n = 5000
	}
	return strconv.Itoa(n)
}

func moduleJournalRecent(ctx context.Context, args map[string]string) ([]byte, int) {
	n := "80"
	if v := strings.TrimSpace(args["lines"]); v != "" {
		n = moduleJournalLines(v)
	}
	switch runtime.GOOS {
	case "linux":
		if have("journalctl") {
			return runModuleCmds(ctx, [][]string{{"journalctl", "-n", n, "--no-pager", "-o", "short-iso"}})
		}
		return runModuleCmds(ctx, [][]string{{"tail", "-n", n, "/var/log/messages"}, {"tail", "-n", n, "/var/log/syslog"}})
	case "darwin":
		return runModuleCmds(ctx, [][]string{{"log", "show", "--last", "30m", "--style", "compact"}})
	case "windows":
		return runModuleCmds(ctx, [][]string{{"cmd", "/c", "wevtutil qe System /c:" + n + " /f:text"}})
	}
	return []byte("不支持的系统"), 1
}

func moduleDmesgRecent(ctx context.Context) ([]byte, int) {
	switch runtime.GOOS {
	case "linux", "darwin":
		return runModuleCmds(ctx, [][]string{{"dmesg", "-T"}})
	default:
		return []byte("当前系统无 dmesg"), 1
	}
}

func moduleNetIfaces() ([]byte, int) {
	var b strings.Builder
	ifaces, err := net.Interfaces()
	if err != nil {
		return []byte(err.Error()), 1
	}
	for _, ifc := range ifaces {
		fmt.Fprintf(&b, "[%s] flags=%s mtu=%d\n", ifc.Name, ifc.Flags.String(), ifc.MTU)
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			fmt.Fprintf(&b, "  addr=%s\n", a.String())
		}
	}
	return []byte(b.String()), 0
}

func moduleNetListen(ctx context.Context) ([]byte, int) {
	switch runtime.GOOS {
	case "windows":
		return runModuleCmds(ctx, [][]string{{"cmd", "/c", "netstat -ano"}})
	case "darwin":
		// macOS netstat has no Linux-style -p PID; prefer lsof for TCP listeners.
		if have("lsof") {
			out, code := runModuleCmds(ctx, [][]string{{"lsof", "-nP", "-iTCP", "-sTCP:LISTEN"}})
			if code == 0 && len(strings.TrimSpace(string(out))) > 0 {
				return out, 0
			}
		}
		return runModuleCmds(ctx, [][]string{{"netstat", "-anv", "-p", "tcp"}, {"netstat", "-anv", "-p", "udp"}})
	default:
		if have("ss") {
			return runModuleCmds(ctx, [][]string{{"ss", "-lntup"}})
		}
		return runModuleCmds(ctx, [][]string{{"netstat", "-lntp"}})
	}
}

func moduleNetRoutes(ctx context.Context) ([]byte, int) {
	switch runtime.GOOS {
	case "windows":
		return runModuleCmds(ctx, [][]string{{"route", "print"}})
	case "darwin":
		return runModuleCmds(ctx, [][]string{{"netstat", "-rn"}})
	default:
		if have("ip") {
			return runModuleCmds(ctx, [][]string{{"ip", "route"}})
		}
		return runModuleCmds(ctx, [][]string{{"route", "-n"}})
	}
}

func moduleNetSockets(ctx context.Context) ([]byte, int) {
	switch runtime.GOOS {
	case "windows":
		return runModuleCmds(ctx, [][]string{{"cmd", "/c", "netstat -an"}})
	default:
		if have("ss") {
			return runModuleCmds(ctx, [][]string{{"ss", "-s"}, {"ss", "-ant"}})
		}
		return runModuleCmds(ctx, [][]string{{"netstat", "-an"}})
	}
}

func moduleDNSResolve(args map[string]string) ([]byte, int) {
	host := strings.TrimSpace(args["host"])
	if host == "" {
		return []byte("dns_resolve 缺少 host"), 1
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return []byte("解析失败: " + err.Error()), 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "host=%s\n", host)
	for _, ip := range ips {
		fmt.Fprintf(&b, "ip=%s\n", ip.String())
	}
	return []byte(b.String()), 0
}

func moduleDockerPS(ctx context.Context) ([]byte, int) {
	cli := containerCLI()
	if cli == "" {
		// Soft-skip: no runtime is common on bare hosts; exit 0 so playbooks don't fail.
		return []byte("skip: 未找到 docker/podman，跳过容器列表\n"), 0
	}
	return runModuleCmds(ctx, [][]string{{cli, "ps", "-a", "--format", "table {{.ID}}\t{{.Names}}\t{{.Status}}\t{{.Image}}\t{{.Ports}}"}})
}

func moduleDockerStats(ctx context.Context) ([]byte, int) {
	cli := containerCLI()
	if cli == "" {
		return []byte("skip: 未找到 docker/podman，跳过容器资源\n"), 0
	}
	return runModuleCmds(ctx, [][]string{{cli, "stats", "--no-stream", "--format", "table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.NetIO}}"}})
}

func moduleKubeGet(ctx context.Context, args map[string]string) ([]byte, int) {
	if !have("kubectl") {
		return []byte("skip: 未找到 kubectl，跳过 K8s 查询\n"), 0
	}
	res := strings.TrimSpace(args["resource"])
	if res == "" {
		res = "pods"
	}
	// 只允许只读 get 子资源名字符
	for _, c := range res {
		if !(c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return []byte("非法 resource 参数"), 1
		}
	}
	return runModuleCmds(ctx, [][]string{{"kubectl", "get", res, "-A", "-o", "wide"}})
}

func moduleTimeSync(ctx context.Context) ([]byte, int) {
	var b strings.Builder
	fmt.Fprintf(&b, "now=%s\n", time.Now().Format(time.RFC3339Nano))
	fmt.Fprintf(&b, "unix=%d\n", time.Now().Unix())
	zone, off := time.Now().Zone()
	fmt.Fprintf(&b, "zone=%s offset_sec=%d\n", zone, off)
	switch runtime.GOOS {
	case "linux":
		if have("timedatectl") {
			out, _ := runModuleCmds(ctx, [][]string{{"timedatectl", "status"}})
			b.Write(out)
		}
	}
	return []byte(b.String()), 0
}

func moduleUsersLogged(ctx context.Context) ([]byte, int) {
	switch runtime.GOOS {
	case "windows":
		return runModuleCmds(ctx, [][]string{{"query", "user"}})
	default:
		return runModuleCmds(ctx, [][]string{{"who"}, {"w"}})
	}
}

func moduleSecurityListen(ctx context.Context) ([]byte, int) {
	// 与 net_listen 相同数据源，附加说明头
	out, exit := moduleNetListen(ctx)
	head := []byte("# security_listen: 对外监听端口（只读）\n")
	return append(head, out...), exit
}

func moduleAuthFailures(ctx context.Context) ([]byte, int) {
	switch runtime.GOOS {
	case "linux":
		if have("journalctl") {
			return runModuleCmds(ctx, [][]string{{"journalctl", "-n", "50", "--no-pager", "-u", "sshd", "-g", "Failed|Invalid|authentication failure"}})
		}
		return runModuleCmds(ctx, [][]string{{"grep", "-E", "Failed|Invalid|authentication failure", "/var/log/auth.log"}, {"tail", "-n", "50", "/var/log/secure"}})
	case "darwin":
		return runModuleCmds(ctx, [][]string{{"log", "show", "--last", "1h", "--predicate", "eventMessage CONTAINS \"Authentication\"", "--style", "compact"}})
	default:
		return []byte("当前系统暂无统一认证失败日志接口"), 1
	}
}

func moduleBigdataJPS(ctx context.Context) ([]byte, int) {
	if !have("jps") {
		return []byte("未找到 jps（需 JDK）"), 1
	}
	return runModuleCmds(ctx, [][]string{{"jps", "-lvm"}})
}

func moduleBigdataPorts(ctx context.Context) ([]byte, int) {
	// 常见 Hadoop/Spark/Kafka/ES 端口探测（只看本机监听）
	ports := []string{"8020", "8088", "9000", "9870", "9864", "2181", "9092", "9200", "9300", "7077", "8080", "18080"}
	var b strings.Builder
	b.WriteString("# bigdata_ports: 检查本机是否监听常见大数据端口\n")
	listenOut, _ := moduleNetListen(ctx)
	listen := string(listenOut)
	for _, p := range ports {
		hit := strings.Contains(listen, ":"+p) || strings.Contains(listen, "."+p+" ")
		fmt.Fprintf(&b, "port=%s listening=%v\n", p, hit)
	}
	return []byte(b.String()), 0
}
