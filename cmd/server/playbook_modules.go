package main

import (
	"fmt"
	"regexp"
	"strings"
)

// playbookModuleMeta describes a built-in playbook module.
// ReadOnly modules never mutate the host; write modules (service/package/copy) can.
type playbookModuleMeta struct {
	Name        string
	ReadOnly    bool
	Domain      string // system | network | sre | security | bigdata | change
	RequiredArg string // if set, args[RequiredArg] must be non-empty
	Desc        string
}

// knownPlaybookModules is the server-side catalog. Agent must implement the same names.
var knownPlaybookModules = map[string]playbookModuleMeta{
	// —— 系统运维（只读）——
	"gather_facts":   {Name: "gather_facts", ReadOnly: true, Domain: "system", Desc: "采集主机名/OS/架构/CPU/IP/内存摘要"},
	"host_inspect":   {Name: "host_inspect", ReadOnly: true, Domain: "system", Desc: "深度主机巡检（结构化 JSON 报告；args.profile=quick|standard|deep）"},
	"disk_usage":     {Name: "disk_usage", ReadOnly: true, Domain: "system", Desc: "跨平台磁盘用量（Linux df / Windows CIM / macOS df）"},
	"mem_info":       {Name: "mem_info", ReadOnly: true, Domain: "system", Desc: "内存与交换区概况（Windows 使用 CIM）"},
	"cpu_load":       {Name: "cpu_load", ReadOnly: true, Domain: "system", Desc: "负载与 CPU 概况（Windows 使用 CIM）"},
	"process_top":    {Name: "process_top", ReadOnly: true, Domain: "system", Desc: "占用最高的进程列表"},
	"uptime_info":    {Name: "uptime_info", ReadOnly: true, Domain: "system", Desc: "运行时长与登录用户数"},
	"pkg_list":       {Name: "pkg_list", ReadOnly: true, Domain: "system", Desc: "已安装软件包摘要（只读；Windows 优先 winget/Get-Package）"},
	"file_stat":      {Name: "file_stat", ReadOnly: true, Domain: "system", RequiredArg: "path", Desc: "查看文件/目录元数据（不读内容）"},
	"file_head":      {Name: "file_head", ReadOnly: true, Domain: "system", RequiredArg: "path", Desc: "读取文本文件开头（敏感路径拦截）"},
	"service_status": {Name: "service_status", ReadOnly: true, Domain: "system", RequiredArg: "name", Desc: "查询服务状态（不启停）"},
	"journal_recent": {Name: "journal_recent", ReadOnly: true, Domain: "sre", Desc: "最近系统日志（journalctl/事件）"},
	"dmesg_recent":   {Name: "dmesg_recent", ReadOnly: true, Domain: "sre", Desc: "最近内核消息"},

	// —— 网络运维（只读）——
	"net_ifaces":  {Name: "net_ifaces", ReadOnly: true, Domain: "network", Desc: "网卡与 IPv4 地址"},
	"net_listen":  {Name: "net_listen", ReadOnly: true, Domain: "network", Desc: "监听端口列表"},
	"net_routes":  {Name: "net_routes", ReadOnly: true, Domain: "network", Desc: "路由表摘要"},
	"net_sockets": {Name: "net_sockets", ReadOnly: true, Domain: "network", Desc: "连接/套接字摘要"},
	"dns_resolve": {Name: "dns_resolve", ReadOnly: true, Domain: "network", RequiredArg: "host", Desc: "DNS 解析探测（只读）"},

	// —— SRE / 可观测（只读）——
	"docker_ps":    {Name: "docker_ps", ReadOnly: true, Domain: "sre", Desc: "容器列表（docker ps）"},
	"docker_stats": {Name: "docker_stats", ReadOnly: true, Domain: "sre", Desc: "容器资源瞬时占用"},
	"kube_get":     {Name: "kube_get", ReadOnly: true, Domain: "sre", Desc: "kubectl get（默认 pods -A）"},
	"time_sync":    {Name: "time_sync", ReadOnly: true, Domain: "sre", Desc: "系统时间与时区"},

	// —— 安全运维（只读）——
	"users_logged":       {Name: "users_logged", ReadOnly: true, Domain: "security", Desc: "当前登录会话"},
	"security_listen":    {Name: "security_listen", ReadOnly: true, Domain: "security", Desc: "对外监听端口（安全视角）"},
	"host_security_scan": {Name: "host_security_scan", ReadOnly: true, Domain: "security", Desc: "主机安全扫描（包清单/加固/IOC/ClamAV 可选）"},
	"auth_failures":      {Name: "auth_failures", ReadOnly: true, Domain: "security", Desc: "近期认证失败摘要（若可得）"},
	"clamav_update":      {Name: "clamav_update", ReadOnly: false, Domain: "security", Desc: "更新 ClamAV 病毒库（freshclam；args.proxy=host:port 走代理，timeout_sec 可调）"},

	// —— Java 应用运维（只读）——
	//
	// 这几个模块都做**判读**而不是回传原始命令输出：一份 jstack 是几千行栈、一次 jstat
	// 只给自启动以来的累计值，两者原样回传对排障几乎没有价值。Agent 侧会算出窗口内的
	// GC 增量、线程状态分布与栈顶聚类、异常按类聚合，并以「发现:」行标出可疑点——
	// 那些行同时也是 AI 诊断的输入。
	"java_processes":      {Name: "java_processes", ReadOnly: true, Domain: "java", Desc: "JVM 进程清单与启动参数判读（堆上限/收集器/OOM 转储开关；args.full=1 附原始参数）"},
	"java_jvm_info":       {Name: "java_jvm_info", ReadOnly: true, Domain: "java", Desc: "JVM 运行时生效参数（jinfo；args.pid|name 指定进程，args.sysprops=1 附系统属性）"},
	"java_gc_stat":        {Name: "java_gc_stat", ReadOnly: true, Domain: "java", Desc: "GC 健康度：多次采样算窗口内 YGC/FGC 增量与老年代回收效果（args.interval_ms/count）"},
	"java_thread_dump":    {Name: "java_thread_dump", ReadOnly: true, Domain: "java", Desc: "线程转储判读：死锁检测、状态分布、栈顶聚类、线程池饱和（args.full=1 附原文）"},
	"java_heap_histo":     {Name: "java_heap_histo", ReadOnly: true, Domain: "java", Desc: "堆对象直方图 Top-N（args.top；**args.live=1 会触发 Full GC 停顿**，默认关闭）"},
	"java_exception_scan": {Name: "java_exception_scan", ReadOnly: true, Domain: "java", Desc: "应用日志异常按类聚合，OOM/StackOverflow 提级（args.path 指定日志，默认自动发现）"},
	"java_app_inspect":    {Name: "java_app_inspect", ReadOnly: true, Domain: "java", Desc: "一站式 Java 巡检：进程/参数/GC/线程/异常五项串跑并汇总「发现」，可直接交 AI 诊断"},

	// —— 大数据运维（只读）——
	"bigdata_jps":   {Name: "bigdata_jps", ReadOnly: true, Domain: "bigdata", Desc: "Java 进程列表（jps）"},
	"bigdata_ports": {Name: "bigdata_ports", ReadOnly: true, Domain: "bigdata", Desc: "常见大数据端口监听检查"},

	// —— 变更类（会修改系统，保留兼容）——
	"service":      {Name: "service", ReadOnly: false, Domain: "change", RequiredArg: "name", Desc: "启停/重启服务"},
	"package":      {Name: "package", ReadOnly: false, Domain: "change", RequiredArg: "name", Desc: "安装/卸载软件包"},
	"copy":         {Name: "copy", ReadOnly: false, Domain: "change", RequiredArg: "dest", Desc: "写入文件"},
	"agent_update": {Name: "agent_update", ReadOnly: false, Domain: "change", Desc: "从服务端 /dl 下载并热更新 Agent（SHA-256 校验；args.server 必填；rollback=1 回滚 .bak）"},

	// —— 资源管控（虚拟机 / 容器）——
	"hyperv_power":         {Name: "hyperv_power", ReadOnly: false, Domain: "change", Desc: "Hyper-V 虚拟机启停/重启（args.action + vm_id|name）"},
	"hyperv_set":           {Name: "hyperv_set", ReadOnly: false, Domain: "change", Desc: "Hyper-V 调整 CPU/内存（processor_count / memory_mb / min/max / dynamic_memory）"},
	"container_action":     {Name: "container_action", ReadOnly: false, Domain: "change", Desc: "Docker/Podman 容器启停/重启"},
	"container_compose_ls": {Name: "container_compose_ls", ReadOnly: true, Domain: "inspect", Desc: "列出 Docker/Podman Compose 项目"},
	"container_compose":    {Name: "container_compose", ReadOnly: false, Domain: "change", Desc: "Compose up/down/ps/logs/pull（需 project 或 file）"},
	"container_logs":       {Name: "container_logs", ReadOnly: true, Domain: "sre", Desc: "Docker/Podman 容器日志"},
}

func validatePlaybookModule(st PlaybookStep) error {
	mod := strings.TrimSpace(st.Module)
	meta, ok := knownPlaybookModules[mod]
	if !ok {
		return fmt.Errorf("未知模块 %q（请使用内置只读/变更模块）", mod)
	}
	if meta.RequiredArg != "" {
		if st.Args == nil || strings.TrimSpace(st.Args[meta.RequiredArg]) == "" {
			return fmt.Errorf("模块 %s 缺少必填参数 %s", mod, meta.RequiredArg)
		}
	}
	if mod == "file_head" || mod == "file_stat" {
		path := ""
		if st.Args != nil {
			path = st.Args["path"]
		}
		if deniedSensitivePath(path) {
			return fmt.Errorf("模块 %s 拒绝访问敏感路径", mod)
		}
	}
	if mod == "copy" {
		dest := ""
		if st.Args != nil {
			dest = st.Args["dest"]
		}
		if deniedSensitivePath(dest) {
			return fmt.Errorf("copy 拒绝写入敏感路径")
		}
	}
	if mod == "host_inspect" && st.Args != nil {
		p := strings.ToLower(strings.TrimSpace(st.Args["profile"]))
		if p != "" && p != "quick" && p != "standard" && p != "deep" {
			return fmt.Errorf("host_inspect profile 须为 quick / standard / deep")
		}
	}
	return nil
}

func deniedSensitivePath(p string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return false
	}
	deny := []string{
		"/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/master.passwd",
		".ssh/", ".gnupg/", ".aws/", ".kube/config",
		"/root/.bash_history", "\\sam", "\\system32\\config\\sam",
	}
	for _, d := range deny {
		if strings.Contains(p, d) {
			return true
		}
	}
	return false
}

// validatePlaybookCommands returns an error if any step fails the policy in a blocking way.
// Module steps skip shell Command emptiness; shell steps validate Command + OS overrides.
func validatePlaybookCommands(steps []PlaybookStep, pol CmdPolicyConfig) error {
	for i, st := range steps {
		name := st.Name
		if name == "" {
			name = fmt.Sprintf("#%d", i+1)
		}
		if strings.TrimSpace(st.Module) != "" {
			if err := validatePlaybookModule(st); err != nil {
				return fmt.Errorf("步骤 %s: %s", name, err.Error())
			}
		} else {
			cmds := []struct {
				label string
				cmd   string
			}{
				{"", st.Command},
				{"Windows 覆盖", st.CommandWin},
				{"macOS 覆盖", st.CommandMac},
			}
			any := false
			for _, c := range cmds {
				if strings.TrimSpace(c.cmd) == "" {
					continue
				}
				any = true
				ok, _, reason := evaluatePlaybookCommand(c.cmd, pol)
				if !ok {
					if c.label != "" {
						return fmt.Errorf("步骤 %s (%s): %s", name, c.label, reason)
					}
					return fmt.Errorf("步骤 %s: %s", name, reason)
				}
			}
			if !any {
				return fmt.Errorf("步骤 %s: 命令不能为空", name)
			}
		}

		// Rollback is executable code too; validate every OS variant with the
		// same command policy at save time and again at execution time.
		for _, rb := range []struct {
			label string
			cmd   string
		}{
			{"回滚", st.Rollback},
			{"Windows 回滚", st.RollbackWin},
			{"macOS 回滚", st.RollbackMac},
		} {
			if strings.TrimSpace(rb.cmd) == "" {
				continue
			}
			ok, _, reason := evaluatePlaybookCommand(rb.cmd, pol)
			if !ok {
				return fmt.Errorf("步骤 %s (%s): %s", name, rb.label, reason)
			}
		}
	}
	return nil
}

// validatePlaybookVariables rejects typoed/forward-referenced variables rather
// than silently replacing them with an empty string at execution time.
func validatePlaybookVariables(steps []PlaybookStep) error {
	known := map[string]bool{
		"host_id": true, "hostname": true, "ip": true, "os": true, "category": true,
	}
	check := func(stepName, field, value string) error {
		for _, match := range pbVarRE.FindAllStringSubmatch(value, -1) {
			if len(match) > 1 && !known[match[1]] {
				return fmt.Errorf("步骤 %s: %s 引用了未知或尚未注册的变量 %q", stepName, field, match[1])
			}
		}
		return nil
	}
	for i, st := range steps {
		name := strings.TrimSpace(st.Name)
		if name == "" {
			name = fmt.Sprintf("#%d", i+1)
		}
		values := []struct {
			field string
			value string
		}{
			{"command", st.Command}, {"command_win", st.CommandWin}, {"command_mac", st.CommandMac},
			{"when", st.When}, {"rollback", st.Rollback}, {"rollback_win", st.RollbackWin},
			{"rollback_mac", st.RollbackMac},
		}
		for k, v := range st.Args {
			values = append(values, struct {
				field string
				value string
			}{"args." + k, v})
		}
		for _, v := range values {
			if err := check(name, v.field, v.value); err != nil {
				return err
			}
		}
		if st.Register != "" {
			reg := strings.TrimSpace(st.Register)
			if !regexp.MustCompile(`^[a-zA-Z_]\w*$`).MatchString(reg) {
				return fmt.Errorf("步骤 %s: register 变量名 %q 无效", name, st.Register)
			}
			known[reg] = true
		}
	}
	return nil
}

// playbookNeedsForcedApproval reports whether any step suggests forcing approval.
func playbookNeedsForcedApproval(steps []PlaybookStep, pol CmdPolicyConfig) bool {
	for _, st := range steps {
		if strings.TrimSpace(st.Module) != "" {
			meta, ok := knownPlaybookModules[st.Module]
			if ok && !meta.ReadOnly {
				return true // 变更类模块默认建议审批
			}
			continue
		}
		for _, cmd := range []string{st.Command, st.CommandWin, st.CommandMac} {
			if strings.TrimSpace(cmd) == "" {
				continue
			}
			_, force, _ := evaluatePlaybookCommand(cmd, pol)
			if force {
				return true
			}
		}
		for _, cmd := range []string{st.Rollback, st.RollbackWin, st.RollbackMac} {
			if strings.TrimSpace(cmd) == "" {
				continue
			}
			_, force, _ := evaluatePlaybookCommand(cmd, pol)
			if force {
				return true
			}
		}
	}
	return false
}
