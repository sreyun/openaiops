package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// modulePrefix 标识一条「内置模块调用」封套命令。必须与服务端 cmd/server/playbook_api.go
// 中的同名常量保持一致。服务端把模块步骤编码成 modulePrefix+" "+JSON 下发，Agent 识别后
// 直接用 Go 执行对应模块（跨系统一致、无需运维背命令），复用现有 exec 通道与退出码机制。
const modulePrefix = "__AIOPS_MODULE__"

// moduleCall 是模块调用封套的 JSON 结构。
type moduleCall struct {
	Module string            `json:"module"`
	Args   map[string]string `json:"args"`
}

// runModule 解析封套并分派到对应内置模块，返回合并输出与退出码（0=成功）。
func (a *Agent) runModule(payload string) (out []byte, code int) {
	return a.runModuleCtx(context.Background(), payload)
}

func (a *Agent) runModuleCtx(ctx context.Context, payload string) (out []byte, code int) {
	defer func() {
		if r := recover(); r != nil {
			out = []byte(fmt.Sprintf("模块执行异常(已隔离): %v", r))
			code = 1
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return []byte("（剧本已停止）"), 130
	}
	var mc moduleCall
	if err := json.Unmarshal([]byte(payload), &mc); err != nil {
		return []byte("模块参数解析失败: " + err.Error()), 1
	}
	switch mc.Module {
	case "gather_facts":
		return moduleGatherFacts()
	case "host_inspect":
		return moduleHostInspectCtx(ctx, mc.Args)
	case "disk_usage":
		return moduleDiskUsage()
	case "mem_info":
		return moduleMemInfo()
	case "cpu_load":
		return moduleCPULoad()
	case "process_top":
		return moduleProcessTop()
	case "uptime_info":
		return moduleUptimeInfo()
	case "pkg_list":
		return modulePkgList()
	case "file_stat":
		return moduleFileStat(mc.Args)
	case "file_head":
		return moduleFileHead(mc.Args)
	case "service_status":
		return moduleServiceStatus(mc.Args)
	case "journal_recent":
		return moduleJournalRecent(mc.Args)
	case "dmesg_recent":
		return moduleDmesgRecent()
	case "net_ifaces":
		return moduleNetIfaces()
	case "net_listen":
		return moduleNetListen()
	case "net_routes":
		return moduleNetRoutes()
	case "net_sockets":
		return moduleNetSockets()
	case "dns_resolve":
		return moduleDNSResolve(mc.Args)
	case "docker_ps":
		return moduleDockerPS()
	case "docker_stats":
		return moduleDockerStats()
	case "kube_get":
		return moduleKubeGet(mc.Args)
	case "hyperv_power":
		return moduleHyperVPower(mc.Args)
	case "hyperv_set":
		return moduleHyperVSet(mc.Args)
	case "container_action":
		return moduleContainerAction(mc.Args)
	case "container_logs":
		return moduleContainerLogs(mc.Args)
	case "container_exec":
		return moduleContainerExec(mc.Args)
	case "container_compose_ls":
		return moduleComposeList(mc.Args)
	case "container_compose":
		return moduleComposeAction(mc.Args)
	case "time_sync":
		return moduleTimeSync()
	case "users_logged":
		return moduleUsersLogged()
	case "security_listen":
		return moduleSecurityListen()
	case "host_security_scan":
		return moduleHostSecurityScan(mc.Args)
	case "clamav_update":
		return moduleClamavUpdate(mc.Args)
	case "auth_failures":
		return moduleAuthFailures()
	case "bigdata_jps":
		return moduleBigdataJPS()
	case "java_processes":
		return moduleJavaProcesses(mc.Args)
	case "java_jvm_info":
		return moduleJavaJVMInfo(mc.Args)
	case "java_gc_stat":
		return moduleJavaGCStat(mc.Args)
	case "java_thread_dump":
		return moduleJavaThreadDump(mc.Args)
	case "java_heap_histo":
		return moduleJavaHeapHisto(mc.Args)
	case "java_exception_scan":
		return moduleJavaExceptionScan(mc.Args)
	case "java_app_inspect":
		return moduleJavaAppInspect(mc.Args)
	case "bigdata_ports":
		return moduleBigdataPorts()
	case "service":
		return moduleService(mc.Args)
	case "package":
		return modulePackage(mc.Args)
	case "copy":
		return moduleCopy(mc.Args)
	case "agent_update":
		return moduleAgentUpdate(mc.Args, a.allowedUpdateBases())
	default:
		return []byte("未知模块: " + mc.Module), 1
	}
}

// moduleGatherFacts 采集本机基础信息（跨系统一致，只读）。
func moduleGatherFacts() ([]byte, int) {
	var b strings.Builder
	host, _ := os.Hostname()
	ips := localIPv4s()
	first := primaryIP()
	if first == "" && len(ips) > 0 {
		first = ips[0]
	}
	fmt.Fprintf(&b, "hostname=%s\n", host)
	fmt.Fprintf(&b, "os=%s\n", runtime.GOOS)
	fmt.Fprintf(&b, "arch=%s\n", runtime.GOARCH)
	fmt.Fprintf(&b, "cpus=%d\n", runtime.NumCPU())
	fmt.Fprintf(&b, "ip=%s\n", first)
	fmt.Fprintf(&b, "ips=%s\n", strings.Join(ips, ", "))
	fmt.Fprintf(&b, "now=%s\n", time.Now().Format(time.RFC3339))
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Fprintf(&b, "go_alloc_mb=%.1f\n", float64(ms.Alloc)/1024/1024)
	if runtime.GOOS == "linux" {
		d := detectLinuxDistro()
		fmt.Fprintf(&b, "platform=%s\n", d.Pretty)
		fmt.Fprintf(&b, "os_family=%s\n", d.Family)
		fmt.Fprintf(&b, "distro=%s\n", d.ID)
		fmt.Fprintf(&b, "distro_version=%s\n", d.Version)
		fmt.Fprintf(&b, "pkg_family=%s\n", d.Pkg)
		if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
			fmt.Fprintf(&b, "loadavg=%s", string(raw))
		}
		if raw, err := os.ReadFile("/proc/uptime"); err == nil {
			fmt.Fprintf(&b, "uptime_sec_raw=%s", string(raw))
		}
	}
	return []byte(b.String()), 0
}

// localIPv4s 返回已启用、非回环网卡的 IPv4，按可用性排序（真实局域网/公网优先，169.254 靠后）。
func localIPv4s() []string {
	return rankedLocalIPv4s()
}

// moduleService 管理系统服务。参数：name（必填）、state（started/stopped/restarted/reloaded，
// 默认 started）、enabled（true/false，可选，控制开机自启）。按系统选择 systemctl/sc/brew。
func moduleService(args map[string]string) ([]byte, int) {
	name := strings.TrimSpace(args["name"])
	if name == "" {
		return []byte("service 模块缺少 name 参数"), 1
	}
	state := strings.ToLower(strings.TrimSpace(args["state"]))
	if state == "" {
		state = "started"
	}
	enabled := strings.ToLower(strings.TrimSpace(args["enabled"]))

	var cmds [][]string
	switch runtime.GOOS {
	case "linux":
		switch state {
		case "started":
			cmds = append(cmds, []string{"systemctl", "start", name})
		case "stopped":
			cmds = append(cmds, []string{"systemctl", "stop", name})
		case "restarted":
			cmds = append(cmds, []string{"systemctl", "restart", name})
		case "reloaded":
			cmds = append(cmds, []string{"systemctl", "reload", name})
		default:
			return []byte("未知 state: " + state), 1
		}
		switch enabled {
		case "true":
			cmds = append(cmds, []string{"systemctl", "enable", name})
		case "false":
			cmds = append(cmds, []string{"systemctl", "disable", name})
		}
	case "windows":
		switch state {
		case "started":
			cmds = append(cmds, []string{"sc", "start", name})
		case "stopped":
			cmds = append(cmds, []string{"sc", "stop", name})
		case "restarted", "reloaded":
			cmds = append(cmds, []string{"sc", "stop", name}, []string{"sc", "start", name})
		default:
			return []byte("未知 state: " + state), 1
		}
		switch enabled {
		case "true":
			cmds = append(cmds, []string{"sc", "config", name, "start=", "auto"})
		case "false":
			cmds = append(cmds, []string{"sc", "config", name, "start=", "demand"})
		}
	case "darwin":
		action := map[string]string{"started": "start", "stopped": "stop", "restarted": "restart", "reloaded": "restart"}[state]
		if action == "" {
			return []byte("未知 state: " + state), 1
		}
		cmds = append(cmds, []string{"brew", "services", action, name})
	default:
		return []byte("service 模块不支持当前系统: " + runtime.GOOS), 1
	}
	return runModuleCmds(cmds)
}

// modulePackage 安装/卸载软件包。参数：name（必填）、state（present/installed/latest=安装，
// absent/removed=卸载；默认 present）。自动探测系统包管理器。
func modulePackage(args map[string]string) ([]byte, int) {
	name := strings.TrimSpace(args["name"])
	if name == "" {
		return []byte("package 模块缺少 name 参数"), 1
	}
	state := strings.ToLower(strings.TrimSpace(args["state"]))
	install := state != "absent" && state != "removed"
	argv, err := packageArgv(install, name)
	if err != nil {
		return []byte(err.Error()), 1
	}
	return runModuleCmds([][]string{argv})
}

// packageArgv 依据系统与已安装的包管理器，返回安装/卸载某包的命令行参数。
func packageArgv(install bool, name string) ([]string, error) {
	switch runtime.GOOS {
	case "linux":
		// Rocky 9/10、麒麟 V10/V11 等按发行版包族选择，避免错误命中 apt-get 桩。
		mgr := linuxPkgManagerCmd()
		switch mgr {
		case "apt-get", "apt":
			if install {
				return []string{"apt-get", "install", "-y", name}, nil
			}
			return []string{"apt-get", "remove", "-y", name}, nil
		case "dnf":
			if install {
				return []string{"dnf", "install", "-y", name}, nil
			}
			return []string{"dnf", "remove", "-y", name}, nil
		case "yum":
			if install {
				return []string{"yum", "install", "-y", name}, nil
			}
			return []string{"yum", "remove", "-y", name}, nil
		case "apk":
			if install {
				return []string{"apk", "add", name}, nil
			}
			return []string{"apk", "del", name}, nil
		case "zypper":
			if install {
				return []string{"zypper", "--non-interactive", "install", name}, nil
			}
			return []string{"zypper", "--non-interactive", "remove", name}, nil
		case "pacman":
			if install {
				return []string{"pacman", "-S", "--noconfirm", name}, nil
			}
			return []string{"pacman", "-R", "--noconfirm", name}, nil
		}
		return nil, fmt.Errorf("未找到受支持的包管理器 (apt/dnf/yum/apk/zypper/pacman)；当前发行版可能未正确识别")
	case "darwin":
		if !have("brew") {
			return nil, fmt.Errorf("未找到 brew，请先安装 Homebrew")
		}
		if install {
			return []string{"brew", "install", name}, nil
		}
		return []string{"brew", "uninstall", name}, nil
	case "windows":
		if have("choco") {
			if install {
				return []string{"choco", "install", "-y", name}, nil
			}
			return []string{"choco", "uninstall", "-y", name}, nil
		}
		if have("winget") {
			if install {
				return []string{"winget", "install", "--silent", "--accept-package-agreements", "--accept-source-agreements", name}, nil
			}
			return []string{"winget", "uninstall", "--silent", name}, nil
		}
		return nil, fmt.Errorf("未找到 choco 或 winget")
	}
	return nil, fmt.Errorf("package 模块不支持当前系统: %s", runtime.GOOS)
}

// moduleCopy 把内容写入目标文件（自动创建父目录）。参数：dest（必填）、content、mode（八进制，
// 如 0644，默认 0644）。跨系统一致，无需 echo/重定向。
func moduleCopy(args map[string]string) ([]byte, int) {
	dest := strings.TrimSpace(args["dest"])
	if dest == "" {
		return []byte("copy 模块缺少 dest 参数"), 1
	}
	content := args["content"]
	perm := os.FileMode(0o644)
	if m := strings.TrimSpace(args["mode"]); m != "" {
		if v, err := strconv.ParseUint(m, 8, 32); err == nil {
			perm = os.FileMode(v)
		}
	}
	if dir := filepath.Dir(dest); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	if err := os.WriteFile(dest, []byte(content), perm); err != nil {
		return []byte("写入失败: " + err.Error()), 1
	}
	return []byte(fmt.Sprintf("已写入 %s (%d 字节)", dest, len(content))), 0
}

// runModuleCmds 顺序执行一组命令（非 shell，直接 argv），拼接输出；任一失败即中止并返回其退出码。
func runModuleCmds(cmds [][]string) ([]byte, int) {
	var b bytes.Buffer
	for _, c := range cmds {
		b.WriteString("$ " + strings.Join(c, " ") + "\n")
		out, exit := runArgv(c)
		b.Write(out)
		if n := len(out); n > 0 && out[n-1] != '\n' {
			b.WriteByte('\n')
		}
		if exit != 0 {
			return b.Bytes(), exit
		}
	}
	return b.Bytes(), 0
}

// runArgv 执行单条 argv 命令，返回合并输出与退出码。
func runArgv(argv []string) ([]byte, int) {
	if len(argv) == 0 {
		return nil, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = execEnv()
	out, err := runCmdEscaped(cmd)
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
			out = append(out, []byte("\n"+err.Error())...)
		}
	}
	return out, exit
}

// have 报告某可执行文件是否在 PATH 中。
func have(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
