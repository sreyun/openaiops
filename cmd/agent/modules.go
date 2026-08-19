package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
		return moduleDiskUsage(ctx)
	case "mem_info":
		return moduleMemInfo(ctx)
	case "cpu_load":
		return moduleCPULoad(ctx)
	case "process_top":
		return moduleProcessTop(ctx)
	case "uptime_info":
		return moduleUptimeInfo(ctx)
	case "pkg_list":
		return modulePkgList(ctx)
	case "file_stat":
		return moduleFileStat(mc.Args)
	case "file_head":
		return moduleFileHead(mc.Args)
	case "service_status":
		return moduleServiceStatus(ctx, mc.Args)
	case "journal_recent":
		return moduleJournalRecent(ctx, mc.Args)
	case "dmesg_recent":
		return moduleDmesgRecent(ctx)
	case "net_ifaces":
		return moduleNetIfaces()
	case "net_listen":
		return moduleNetListen(ctx)
	case "net_routes":
		return moduleNetRoutes(ctx)
	case "net_sockets":
		return moduleNetSockets(ctx)
	case "dns_resolve":
		return moduleDNSResolve(mc.Args)
	case "docker_ps":
		return moduleDockerPS(ctx)
	case "docker_stats":
		return moduleDockerStats(ctx)
	case "kube_get":
		return moduleKubeGet(ctx, mc.Args)
	case "hyperv_power":
		return moduleHyperVPower(ctx, mc.Args)
	case "hyperv_set":
		return moduleHyperVSet(ctx, mc.Args)
	case "container_action":
		return moduleContainerAction(ctx, mc.Args)
	case "container_logs":
		return moduleContainerLogs(ctx, mc.Args)
	case "container_exec":
		return moduleContainerExec(ctx, mc.Args)
	case "container_compose_ls":
		return moduleComposeList(ctx, mc.Args)
	case "container_compose":
		return moduleComposeAction(ctx, mc.Args)
	case "time_sync":
		return moduleTimeSync(ctx)
	case "users_logged":
		return moduleUsersLogged(ctx)
	case "security_listen":
		return moduleSecurityListen(ctx)
	case "host_security_scan":
		return moduleHostSecurityScan(ctx, mc.Args)
	case "clamav_update":
		return moduleClamavUpdate(ctx, mc.Args)
	case "auth_failures":
		return moduleAuthFailures(ctx)
	case "bigdata_jps":
		return moduleBigdataJPS(ctx)
	case "java_processes":
		return moduleJavaProcesses(ctx, mc.Args)
	case "java_jvm_info":
		return moduleJavaJVMInfo(ctx, mc.Args)
	case "java_gc_stat":
		return moduleJavaGCStat(ctx, mc.Args)
	case "java_thread_dump":
		return moduleJavaThreadDump(ctx, mc.Args)
	case "java_heap_histo":
		return moduleJavaHeapHisto(ctx, mc.Args)
	case "java_exception_scan":
		return moduleJavaExceptionScan(ctx, mc.Args)
	case "java_app_inspect":
		return moduleJavaAppInspect(ctx, mc.Args)
	case "bigdata_ports":
		return moduleBigdataPorts(ctx)
	case "service":
		return moduleService(ctx, mc.Args)
	case "package":
		return modulePackage(ctx, mc.Args)
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
func moduleService(ctx context.Context, args map[string]string) ([]byte, int) {
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
	return runModuleCmds(ctx, cmds)
}

// modulePackage 安装/卸载软件包。参数：name（必填）、state（present/installed/latest=安装，
// absent/removed=卸载；默认 present）。自动探测系统包管理器。
func modulePackage(ctx context.Context, args map[string]string) ([]byte, int) {
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
	return runModuleCmds(ctx, [][]string{argv})
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

// moduleCombinedOutput 执行一条已经建好的命令并收集输出。
//
// 它只比 cmd.CombinedOutput() 多做一件事：把 exec.ErrWaitDelay 还原成真实结果。设了
// WaitDelay 之后，「进程自己已经正常退出、但它拉起的后台子进程还攥着 stdout 管道」这种
// 情况下 Wait 会返回 ErrWaitDelay——那是「输出可能没收全」，不是「命令失败」。不还原的话，
// 一次成功的 service start（postinst 拉起守护进程是最常见的一种）会被报成失败。
func moduleCombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.CombinedOutput()
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		return out, nil
	}
	return out, err
}

// —— 模块步骤的「停止」语义（所有会起进程的模块都照这三条来）——
//
//  1. 起进程前先看一眼 ctx：已经停了就别再动这台机器；
//  2. 进程挂在**会话 ctx 的派生 ctx** 上（模块自带的超时叠加在它上面），停止能真杀掉它；
//  3. 返回时先判「会话被停止」，再判「本条命令自己超时」——这两件事对运维是不同的结论，
//     都写成「超时」会把人支去查一个不存在的性能问题。
//
// 退出码统一 130，与 shell 步骤（runShellCommandCtx）一致，服务端据此把这步标成「已停止」
// 而不是「失败」。
const moduleStopExit = 130

// moduleStopMsg 是停止时回给面板的固定短句；各模块可在前面拼自己的上下文。
const moduleStopMsg = "（剧本已停止）"

// moduleCtx 归一化 ctx：内部调用点与老测试可能传 nil，而 nil ctx 会让 WithTimeout 直接 panic。
func moduleCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// moduleStopped 报告会话是否已被取消（停止剧本 / 会话超时）。
func moduleStopped(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil
}

// runModuleCmds 顺序执行一组命令（非 shell，直接 argv），拼接输出；任一失败即中止并返回其退出码。
//
// ctx 必须一路带下来：它是「服务端停止了这条剧本」的唯一信号。少了它，停止只是把
// 服务端那边的会话关掉，主机上这一串命令会一条接一条跑完——见 runArgv 的注释。
func runModuleCmds(ctx context.Context, cmds [][]string) ([]byte, int) {
	ctx = moduleCtx(ctx)
	var b bytes.Buffer
	for _, c := range cmds {
		if moduleStopped(ctx) {
			b.WriteString("（剧本已停止，后续命令未执行）\n")
			return b.Bytes(), moduleStopExit
		}
		b.WriteString("$ " + strings.Join(c, " ") + "\n")
		out, exit := runArgv(ctx, c)
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

// moduleMaxOutputBytes 是单条模块命令允许回传的输出上限。
//
// 8 MiB 对任何一条巡检输出都绰绰有余（journalctl -n 5000 约 1 MiB），而它挡住的是
// 另一端：一条没有范围限制的命令在繁忙主机上吐出几百 MB，先撑爆 Agent 内存，再把
// 同样的字节推给服务端。只读巡检把被巡检的机器搞出故障，是最不该发生的事。
const moduleMaxOutputBytes = 8 << 20

// runArgv 执行单条 argv 命令，返回合并输出与退出码。
//
// 传进来的 ctx 是**会话级**的取消信号（服务端停止剧本 / 会话超时 → runExecSession 里的
// watchExecSessionCancel 调 cancel）。此前这里用的是 context.Background()，只有一个 5 分钟
// 的自带超时：于是「停止剧本」对模块步骤是**无效**的——服务端把会话一关就当结束了，主机上
// 这条 apt-get install / jstack / 全盘扫描照跑不误，而且模块常常是一串命令，每条都重新拿到
// 一个完整的 5 分钟预算，一次「已停止」可以在后台拖上几十分钟。shell 步骤一直是对的
// （runShellCommandCtx 收 ctx），只有模块步骤漏了，两者行为不一致比慢更糟：运维按了停止、
// 界面也显示停了，机器却还在被压。
//
// 5 分钟仍作为单条命令的上限叠加在 ctx 之上——会话给 10 分钟，不代表某一条命令可以卡满。
func runArgv(ctx context.Context, argv []string) ([]byte, int) {
	if len(argv) == 0 {
		return nil, 0
	}
	ctx = moduleCtx(ctx)
	if moduleStopped(ctx) {
		return []byte(moduleStopMsg), moduleStopExit
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Env = execEnv()
	// 被杀掉的子进程若把 stdout 交给了自己的子进程（包管理器 postinst 拉起守护进程是最常见
	// 的一种），cmd.Wait 会一直等那根管道关闭：进程早被杀了，「停止」却还卡在这里等。给
	// Wait 一个上限，让停止的耗时有界。
	cmd.WaitDelay = 5 * time.Second
	out, err := runCmdEscapedCapped(cmd, moduleMaxOutputBytes)
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else if errors.Is(err, exec.ErrWaitDelay) {
			// 进程已经自己结束了，只是后台子进程还攥着它的 stdout。以进程自己的退出码
			// 为准，别把「输出没收全」判成「命令失败」。
			if st := cmd.ProcessState; st != nil {
				exit = st.ExitCode()
			}
			out = append(out, []byte("\n（后台子进程仍持有输出管道，输出可能不完整）")...)
		} else {
			exit = -1
			out = append(out, []byte("\n"+err.Error())...)
		}
	}
	// 会话被取消（而不是本条命令自己超时）：进程已被 CommandContext 杀掉，退出码此时是
	// 信号值，原样上报会被当成命令失败。统一成 130，与 shell 步骤一致。
	if moduleStopped(ctx) {
		out = append(out, []byte("\n（剧本已停止，命令被中止）")...)
		return out, moduleStopExit
	}
	return out, exit
}

// have 报告某可执行文件是否在 PATH 中。
func have(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
