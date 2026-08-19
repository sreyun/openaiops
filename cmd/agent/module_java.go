package main

// Java 应用巡检 / 性能分析 / 异常分析。
//
// 为什么不是「跑几条 jstack、jstat 原样回传」：那些命令的原始输出对排障几乎没有价值。
// 一份 jstack 是几千行栈，一份 jstat -gcutil 是一张百分比表——真正决定判断的是从里面
// 算出来的东西：这段时间里 Full GC 发生了几次、每次多久、老年代回收后还剩多少（判泄漏）、
// 有没有死锁、几百个线程卡在同一个栈顶（判阻塞点）。所以这些模块都做**摘要与判读**，
// 原始输出只在需要时（args.full=1）附上。
//
// 兼容性约束：本文件会被 win2012 构建线用 Go 1.20 编译（见 scripts/build-agent-win2012.sh），
// 因此不能用 min/max 内建、slices/maps 标准库、for-range-int 这些 1.21+ 特性。

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// JDK 工具定位
// ---------------------------------------------------------------------------

// javaTool 返回 JDK 工具（jps/jstack/jstat/jinfo/jmap）的可执行路径，找不到返回 ""。
//
// 只查 PATH 是不够的，而且这是现网最常见的"巡检跑不起来"：
//   - 服务以 systemd/SYSTEM 身份运行，PATH 很瘦，JDK 不在里面；
//   - 机器上装的是 JRE，没有这些工具；
//   - 应用自带一份 JDK（容器镜像、中间件安装包），系统 PATH 里的 java 是另一个版本。
//
// 所以顺序是：PATH → JAVA_HOME → **从正在运行的 java 进程反推**。最后一条最有用：
// 目标 JVM 用哪个 JDK 起来的，它的 bin 目录里就有配套的工具，版本也必然匹配
// （jstack 连不同大版本的 JVM 会直接失败）。
func javaTool(name string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		name += ".exe"
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if jh := strings.TrimSpace(os.Getenv("JAVA_HOME")); jh != "" {
		if p := filepath.Join(jh, "bin", name); javaToolAt(p) {
			return p
		}
	}
	for _, dir := range javaBinDirsFromRunningJVMs() {
		if p := filepath.Join(dir, name); javaToolAt(p) {
			return p
		}
	}
	return ""
}

// javaToolAt 判断路径上是不是一个可执行的 JDK 工具。
// 不能复用 fim 里的 fileExecutable：那个查的是 0111 权限位，而 Windows 上 Go 报告的
// 文件权限里根本没有执行位，jstack.exe 会被判成"不可执行"。
func javaToolAt(p string) bool {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return st.Mode()&0o111 != 0
}

// javaBinDirsFromRunningJVMs 从正在运行的 java 进程反查它们的 <jdk>/bin 目录。
func javaBinDirsFromRunningJVMs() []string {
	seen := map[string]bool{}
	var out []string
	add := func(exePath string) {
		exePath = strings.TrimSpace(exePath)
		if exePath == "" {
			return
		}
		dir := filepath.Dir(exePath)
		if dir == "" || dir == "." || seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	switch runtime.GOOS {
	case "linux":
		ents, err := os.ReadDir("/proc")
		if err != nil {
			return nil
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			if _, err := strconv.Atoi(e.Name()); err != nil {
				continue
			}
			link, err := os.Readlink("/proc/" + e.Name() + "/exe")
			if err != nil {
				continue
			}
			if filepath.Base(link) == "java" {
				add(link)
			}
		}
	case "windows":
		// Get-Process 的 Path 属性即映像路径；拿不到（权限不足/进程已退）时静默跳过。
		out2, _ := runArgv([]string{"powershell", "-NoProfile", "-NonInteractive", "-Command",
			"Get-Process java -EA 0 | Select-Object -ExpandProperty Path -Unique"})
		for _, line := range strings.Split(string(out2), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasSuffix(strings.ToLower(line), "java.exe") {
				add(line)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// JVM 进程发现
// ---------------------------------------------------------------------------

type jvmProc struct {
	PID  string
	Main string // 主类或 jar
	Args string // JVM 参数 + 程序参数
}

// listJVMs 列出本机 JVM 进程。jps 不可用时回退到进程表——JRE-only 的机器上
// jps 根本不存在，但巡检"这台机器上跑着哪些 Java 应用"依然要能回答。
func listJVMs() []jvmProc {
	if jps := javaTool("jps"); jps != "" {
		out, _ := runArgv([]string{jps, "-lvm"})
		var res []jvmProc
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fields := strings.SplitN(line, " ", 2)
			pid := fields[0]
			if _, err := strconv.Atoi(pid); err != nil {
				continue
			}
			rest := ""
			if len(fields) > 1 {
				rest = fields[1]
			}
			main, args := rest, ""
			if i := strings.Index(rest, " "); i > 0 {
				main, args = rest[:i], strings.TrimSpace(rest[i+1:])
			}
			// jps 自己也是个 JVM，排掉，否则每次巡检都多一条噪声。
			if main == "jdk.jcmd/sun.tools.jps.Jps" || main == "sun.tools.jps.Jps" || main == "Jps" {
				continue
			}
			res = append(res, jvmProc{PID: pid, Main: main, Args: args})
		}
		if len(res) > 0 {
			return res
		}
	}
	return listJVMsFromProcTable()
}

func listJVMsFromProcTable() []jvmProc {
	var res []jvmProc
	switch runtime.GOOS {
	case "linux":
		ents, err := os.ReadDir("/proc")
		if err != nil {
			return nil
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			pid := e.Name()
			if _, err := strconv.Atoi(pid); err != nil {
				continue
			}
			raw, err := os.ReadFile("/proc/" + pid + "/cmdline")
			if err != nil || len(raw) == 0 {
				continue
			}
			parts := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
			if len(parts) == 0 || filepath.Base(parts[0]) != "java" {
				continue
			}
			res = append(res, jvmProc{PID: pid, Main: javaMainFromArgv(parts), Args: strings.Join(parts[1:], " ")})
		}
	case "windows":
		out, _ := runArgv([]string{"powershell", "-NoProfile", "-NonInteractive", "-Command",
			"Get-CimInstance Win32_Process -Filter \"Name='java.exe'\" | ForEach-Object { \"$($_.ProcessId)`t$($_.CommandLine)\" }"})
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			cols := strings.SplitN(line, "\t", 2)
			if len(cols) < 1 {
				continue
			}
			if _, err := strconv.Atoi(cols[0]); err != nil {
				continue
			}
			cmdline := ""
			if len(cols) > 1 {
				cmdline = cols[1]
			}
			argv := strings.Fields(cmdline)
			res = append(res, jvmProc{PID: cols[0], Main: javaMainFromArgv(argv), Args: cmdline})
		}
	default:
		out, _ := runArgv([]string{"ps", "-axo", "pid,command"})
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			if _, err := strconv.Atoi(fields[0]); err != nil {
				continue
			}
			if filepath.Base(fields[1]) != "java" {
				continue
			}
			res = append(res, jvmProc{PID: fields[0], Main: javaMainFromArgv(fields[1:]), Args: strings.Join(fields[2:], " ")})
		}
	}
	return res
}

// javaMainFromArgv 从 java 命令行里挑出"这是哪个应用"：-jar 的包名，或第一个非选项参数。
func javaMainFromArgv(argv []string) string {
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if a == "-jar" && i+1 < len(argv) {
			return argv[i+1]
		}
		if a == "-cp" || a == "-classpath" || a == "--class-path" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return "java"
}

// resolveJVMPID 解析目标 JVM：显式 pid 优先，其次按主类/jar 名模糊匹配，
// 都没给且机器上只有一个 JVM 时直接用它。
//
// 匹配不到不是"返回空跑一个空结果"，而是**把候选列表回给调用方**——巡检最怕的是
// 一句"未找到"，看的人无从下手。
func resolveJVMPID(args map[string]string, jvms []jvmProc) (string, error) {
	if pid := strings.TrimSpace(args["pid"]); pid != "" {
		if _, err := strconv.Atoi(pid); err != nil {
			return "", fmt.Errorf("pid 不是数字: %q", pid)
		}
		return pid, nil
	}
	if name := strings.TrimSpace(args["name"]); name != "" {
		var hit []jvmProc
		for _, j := range jvms {
			if strings.Contains(strings.ToLower(j.Main+" "+j.Args), strings.ToLower(name)) {
				hit = append(hit, j)
			}
		}
		if len(hit) == 1 {
			return hit[0].PID, nil
		}
		if len(hit) == 0 {
			return "", fmt.Errorf("按 name=%q 未匹配到 JVM 进程\n%s", name, jvmCandidateList(jvms))
		}
		return "", fmt.Errorf("按 name=%q 匹配到 %d 个 JVM，请用 pid 指定\n%s", name, len(hit), jvmCandidateList(hit))
	}
	if len(jvms) == 1 {
		return jvms[0].PID, nil
	}
	if len(jvms) == 0 {
		return "", fmt.Errorf("本机未发现运行中的 JVM 进程")
	}
	return "", fmt.Errorf("本机有 %d 个 JVM 进程，请用 args.pid 或 args.name 指定\n%s", len(jvms), jvmCandidateList(jvms))
}

func jvmCandidateList(jvms []jvmProc) string {
	var b strings.Builder
	b.WriteString("候选进程：\n")
	for i, j := range jvms {
		if i >= 20 {
			fmt.Fprintf(&b, "  …（另有 %d 个）\n", len(jvms)-i)
			break
		}
		fmt.Fprintf(&b, "  pid=%s  %s\n", j.PID, truncRunes(j.Main, 100))
	}
	return b.String()
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func javaToolMissing(name string) ([]byte, int) {
	return []byte(fmt.Sprintf("未找到 %s：本机 PATH、JAVA_HOME 与运行中的 java 进程目录里都没有该工具。\n"+
		"多数情况是装的是 JRE 而非 JDK，或 Agent 以精简 PATH 的服务身份运行。\n"+
		"处理：安装 JDK，或给 Agent 配置 JAVA_HOME 指向 JDK 根目录。", name)), 1
}

// ---------------------------------------------------------------------------
// java_processes：JVM 进程清单
// ---------------------------------------------------------------------------

// moduleJavaProcesses 列出本机 JVM 并解析出巡检真正要看的那几项：
// 堆上限、GC 收集器、以及是否配了 OOM 时导出堆转储。
//
// 为什么解析而不是回传原始 jps：一行 jps -lvm 输出常常是几百字符的参数串，人眼扫不出
// -Xmx 是多少；而"堆给了多大、用的哪个收集器、OOM 时会不会留下现场"恰恰是每次 Java
// 巡检都要回答的三个问题。
func moduleJavaProcesses(args map[string]string) ([]byte, int) {
	jvms := listJVMs()
	var b strings.Builder
	b.WriteString("# java_processes: 本机 JVM 进程清单（只读）\n")
	if len(jvms) == 0 {
		b.WriteString("未发现运行中的 JVM 进程。\n")
		if javaTool("jps") == "" {
			b.WriteString("提示：本机也没有 jps（可能只装了 JRE），清单已回退到进程表扫描。\n")
		}
		return []byte(b.String()), 0
	}
	fmt.Fprintf(&b, "共 %d 个 JVM 进程\n", len(jvms))
	for _, j := range jvms {
		f := parseJVMFlags(j.Args)
		b.WriteString("\n--- pid=" + j.PID + " ---\n")
		fmt.Fprintf(&b, "应用: %s\n", truncRunes(j.Main, 160))
		fmt.Fprintf(&b, "堆: Xms=%s Xmx=%s  元空间: MaxMetaspace=%s\n", dash(f.Xms), dash(f.Xmx), dash(f.MaxMeta))
		fmt.Fprintf(&b, "收集器: %s\n", dash(f.Collector))
		fmt.Fprintf(&b, "OOM 时导出堆: %v  路径: %s\n", f.HeapDumpOnOOM, dash(f.HeapDumpPath))
		if f.Xmx == "" {
			b.WriteString("发现: 未显式设置 -Xmx，JVM 使用默认上限（容器里可能读错宿主机内存，导致被 OOMKilled）\n")
		}
		if !f.HeapDumpOnOOM {
			b.WriteString("发现: 未开启 -XX:+HeapDumpOnOutOfMemoryError，一旦 OOM 将没有现场可供事后分析\n")
		}
	}
	if strings.TrimSpace(args["full"]) == "1" {
		b.WriteString("\n--- 原始参数 ---\n")
		for _, j := range jvms {
			fmt.Fprintf(&b, "pid=%s %s\n", j.PID, truncRunes(j.Args, 2000))
		}
	}
	return []byte(b.String()), 0
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

type jvmFlags struct {
	Xms, Xmx, MaxMeta string
	Collector         string
	HeapDumpOnOOM     bool
	HeapDumpPath      string
}

func parseJVMFlags(argline string) jvmFlags {
	var f jvmFlags
	var collectors []string
	for _, tok := range strings.Fields(argline) {
		switch {
		case strings.HasPrefix(tok, "-Xms"):
			f.Xms = tok[4:]
		case strings.HasPrefix(tok, "-Xmx"):
			f.Xmx = tok[4:]
		case strings.HasPrefix(tok, "-XX:MaxMetaspaceSize="):
			f.MaxMeta = strings.TrimPrefix(tok, "-XX:MaxMetaspaceSize=")
		case tok == "-XX:+HeapDumpOnOutOfMemoryError":
			f.HeapDumpOnOOM = true
		case strings.HasPrefix(tok, "-XX:HeapDumpPath="):
			f.HeapDumpPath = strings.TrimPrefix(tok, "-XX:HeapDumpPath=")
		case strings.HasPrefix(tok, "-XX:+Use") && strings.HasSuffix(tok, "GC"):
			collectors = append(collectors, strings.TrimPrefix(tok, "-XX:+Use"))
		}
	}
	f.Collector = strings.Join(collectors, ",")
	return f
}

// ---------------------------------------------------------------------------
// java_gc_stat：GC 健康度
// ---------------------------------------------------------------------------

// moduleJavaGCStat 采样 jstat -gcutil 并**算出增量**。
//
// 单次 jstat 只能告诉你"此刻各分区水位与累计 GC 次数"，那是自 JVM 启动以来的累计值，
// 对判断"现在有没有问题"几乎无用——一个跑了三个月的进程累计 5 万次 YGC 完全正常。
// 真正有判断力的是这段窗口内的**增量**：这几秒里 Full GC 发生了几次、总共停顿多久、
// 老年代在 Full GC 之后是否降不下去（内存泄漏的典型形态）。
func moduleJavaGCStat(args map[string]string) ([]byte, int) {
	jstat := javaTool("jstat")
	if jstat == "" {
		return javaToolMissing("jstat")
	}
	jvms := listJVMs()
	pid, err := resolveJVMPID(args, jvms)
	if err != nil {
		return []byte(err.Error()), 1
	}
	interval := atoiDefault(args["interval_ms"], 1000)
	if interval < 200 {
		interval = 200
	}
	if interval > 10000 {
		interval = 10000
	}
	count := atoiDefault(args["count"], 6)
	if count < 2 {
		count = 2 // 至少两次采样才有增量可算
	}
	if count > 30 {
		count = 30
	}

	out, exit := runArgv([]string{jstat, "-gcutil", pid, strconv.Itoa(interval), strconv.Itoa(count)})
	raw := string(out)
	if exit != 0 {
		return []byte(javaAttachHint("jstat", pid, raw)), 1
	}
	rows := parseJstatGCUtil(raw)

	var b strings.Builder
	fmt.Fprintf(&b, "# java_gc_stat: pid=%s 采样 %d 次 × %dms（窗口约 %.1fs）\n",
		pid, count, interval, float64(interval*(count-1))/1000)
	if len(rows) < 2 {
		b.WriteString("采样不足，无法计算增量。原始输出：\n" + raw)
		return []byte(b.String()), 1
	}
	first, last := rows[0], rows[len(rows)-1]
	windowSec := float64(interval*(len(rows)-1)) / 1000
	ygc := last.YGC - first.YGC
	ygct := last.YGCT - first.YGCT
	fgc := last.FGC - first.FGC
	fgct := last.FGCT - first.FGCT

	fmt.Fprintf(&b, "\n水位（末次采样）: Eden=%.1f%% Old=%.1f%% Metaspace=%.1f%%\n", last.E, last.O, last.M)
	fmt.Fprintf(&b, "老年代变化: %.1f%% → %.1f%%\n", first.O, last.O)
	fmt.Fprintf(&b, "Young GC: %d 次 / %.3fs（窗口内）", ygc, ygct)
	if ygc > 0 {
		fmt.Fprintf(&b, "，平均 %.1fms/次，频率 %.2f 次/秒", ygct*1000/float64(ygc), float64(ygc)/windowSec)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Full GC: %d 次 / %.3fs（窗口内）", fgc, fgct)
	if fgc > 0 {
		fmt.Fprintf(&b, "，平均 %.1fms/次", fgct*1000/float64(fgc))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "累计（自 JVM 启动）: YGC=%d/%.1fs  FGC=%d/%.1fs\n", last.YGC, last.YGCT, last.FGC, last.FGCT)

	// —— 判读 ——
	b.WriteString("\n判读:\n")
	found := false
	note := func(format string, a ...interface{}) {
		found = true
		fmt.Fprintf(&b, "发现: "+format+"\n", a...)
	}
	if fgc > 0 && windowSec > 0 {
		note("窗口内发生 %d 次 Full GC（%.1fs 内）——Full GC 在健康应用里应当是罕见事件，这个频率会直接体现为接口毛刺", fgc, windowSec)
	}
	if fgc > 0 && last.O > 80 {
		note("Full GC 之后老年代仍在 %.1f%%——回收不下去，是内存泄漏或堆配置过小的典型形态，建议抓一次堆直方图/堆转储比对", last.O)
	} else if last.O > 90 {
		note("老年代水位 %.1f%%，已接近触发 Full GC 的阈值", last.O)
	}
	if ygc > 0 && float64(ygc)/windowSec > 5 {
		note("Young GC 频率 %.1f 次/秒——新生代偏小或对象分配速率过高，会明显抬高 CPU 与停顿占比", float64(ygc)/windowSec)
	}
	if ygc > 0 && ygct*1000/float64(ygc) > 100 {
		note("Young GC 平均停顿 %.0fms，超出常规（通常应在 10ms 量级）", ygct*1000/float64(ygc))
	}
	if last.M > 90 {
		note("元空间水位 %.1f%%——类加载持续增长（动态代理/热部署常见），可能触发 Metaspace OOM", last.M)
	}
	if !found {
		b.WriteString("本窗口内未见异常 GC 特征。\n")
	}
	if strings.TrimSpace(args["full"]) == "1" {
		b.WriteString("\n--- 原始 jstat 输出 ---\n" + raw)
	}
	return []byte(b.String()), 0
}

type gcUtilRow struct {
	E, O, M    float64
	YGC, FGC   int
	YGCT, FGCT float64
}

// parseJstatGCUtil 解析 jstat -gcutil 的表格。列名随 JDK 版本变化（JDK8 有 S0/S1/E/O/M/CCS，
// JDK11+ 相同但顺序偶有差异），所以按**表头名字**取列，不按固定下标。
func parseJstatGCUtil(raw string) []gcUtilRow {
	var head []string
	var rows []gcUtilRow
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if head == nil {
			if strings.EqualFold(fields[0], "S0") || strings.EqualFold(fields[0], "E") {
				head = fields
			}
			continue
		}
		if len(fields) != len(head) {
			continue
		}
		col := func(name string) (float64, bool) {
			for i, h := range head {
				if strings.EqualFold(h, name) {
					v, err := strconv.ParseFloat(fields[i], 64)
					return v, err == nil
				}
			}
			return 0, false
		}
		var r gcUtilRow
		r.E, _ = col("E")
		r.O, _ = col("O")
		r.M, _ = col("M")
		if v, ok := col("YGC"); ok {
			r.YGC = int(v)
		}
		if v, ok := col("FGC"); ok {
			r.FGC = int(v)
		}
		r.YGCT, _ = col("YGCT")
		r.FGCT, _ = col("FGCT")
		rows = append(rows, r)
	}
	return rows
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

// javaAttachHint 把 JDK 工具的 attach 失败翻译成能照做的一句话。
// 原始报错常是 "Unable to open socket file" 或 "well-known file is not secure"，
// 看到的人基本无法从中推出"要用同一个用户跑"。
func javaAttachHint(tool, pid, raw string) string {
	return fmt.Sprintf("%s 连接 pid=%s 失败。\n"+
		"最常见的原因是**身份不一致**：JDK 的 attach 机制要求发起方与目标 JVM 同属一个用户（或为 root）。\n"+
		"Agent 通常以 root/SYSTEM 运行，若目标 JVM 跑在容器内，则要进容器执行（宿主机的 pid 命名空间不同）。\n"+
		"另一类原因是目标 JVM 启用了 -XX:+DisableAttachMechanism。\n\n原始输出：\n%s", tool, pid, raw)
}

// ---------------------------------------------------------------------------
// java_thread_dump：线程与阻塞分析
// ---------------------------------------------------------------------------

// moduleJavaThreadDump 抓一份 jstack 并做聚合判读。
//
// 一份线程转储动辄几千行，直接回传等于把问题原样丢给人。而排障真正要的四件事都能算出来：
// 死锁、线程状态分布、卡在同一个栈顶的线程堆（那就是阻塞点）、以及线程池是否已经打满。
// 默认只回摘要，args.full=1 才附原文。
func moduleJavaThreadDump(args map[string]string) ([]byte, int) {
	jstack := javaTool("jstack")
	if jstack == "" {
		return javaToolMissing("jstack")
	}
	jvms := listJVMs()
	pid, err := resolveJVMPID(args, jvms)
	if err != nil {
		return []byte(err.Error()), 1
	}
	argv := []string{jstack, pid}
	if strings.TrimSpace(args["force"]) == "1" {
		// -F 走的是强制 attach（目标无响应时的最后手段），会让 JVM 暂停更久，故须显式要求。
		argv = []string{jstack, "-l", "-F", pid}
	} else if strings.TrimSpace(args["locks"]) != "0" {
		argv = []string{jstack, "-l", pid} // -l 附带锁信息，死锁与持锁关系全靠它
	}
	out, exit := runArgv(argv)
	raw := string(out)
	if exit != 0 && !strings.Contains(raw, "Full thread dump") {
		return []byte(javaAttachHint("jstack", pid, raw)), 1
	}

	an := analyzeThreadDump(raw)
	var b strings.Builder
	fmt.Fprintf(&b, "# java_thread_dump: pid=%s 线程 %d 个\n", pid, an.Total)
	b.WriteString("\n状态分布:\n")
	for _, kv := range sortedCounts(an.States) {
		fmt.Fprintf(&b, "  %-16s %d\n", kv.Key, kv.N)
	}
	if len(an.Pools) > 0 {
		b.WriteString("\n线程池（按名字前缀聚合，Top 8）:\n")
		for i, kv := range sortedCounts(an.Pools) {
			if i >= 8 {
				break
			}
			fmt.Fprintf(&b, "  %-40s %d\n", truncRunes(kv.Key, 40), kv.N)
		}
	}
	if len(an.TopFrames) > 0 {
		b.WriteString("\n栈顶聚类（Top 8，同一栈顶的线程数）:\n")
		for i, kv := range sortedCounts(an.TopFrames) {
			if i >= 8 {
				break
			}
			fmt.Fprintf(&b, "  %-70s %d\n", truncRunes(kv.Key, 70), kv.N)
		}
	}

	b.WriteString("\n判读:\n")
	found := false
	note := func(format string, a ...interface{}) {
		found = true
		fmt.Fprintf(&b, "发现: "+format+"\n", a...)
	}
	if an.Deadlock != "" {
		note("检测到 Java 级死锁——这是确定性故障，不会自愈，必须重启或改代码。详情：\n%s", truncRunes(an.Deadlock, 2000))
	}
	if blocked := an.States["BLOCKED"]; blocked > 0 {
		pct := float64(blocked) * 100 / float64(maxInt(an.Total, 1))
		if blocked >= 10 || pct >= 20 {
			note("%d 个线程处于 BLOCKED（占 %.0f%%）——存在锁竞争热点，结合上面的栈顶聚类定位持锁方", blocked, pct)
		}
	}
	for _, kv := range sortedCounts(an.TopFrames) {
		if kv.N >= 10 && float64(kv.N) >= float64(an.Total)*0.2 {
			note("%d 个线程卡在同一栈顶 %s——这通常就是瓶颈所在（慢 SQL、外部调用无超时、锁等待）", kv.N, truncRunes(kv.Key, 120))
		}
		break
	}
	if an.Total > 800 {
		note("线程总数 %d 偏高——线程本身占用栈内存（默认 1MB/线程），且会加剧上下文切换", an.Total)
	}
	if !found {
		b.WriteString("未见死锁、明显锁竞争或线程堆积。\n")
	}
	if strings.TrimSpace(args["full"]) == "1" {
		b.WriteString("\n--- 原始 jstack 输出 ---\n" + raw)
	}
	return []byte(b.String()), 0
}

type threadDumpAnalysis struct {
	Total     int
	States    map[string]int
	Pools     map[string]int
	TopFrames map[string]int
	Deadlock  string
}

var threadPoolSuffixRe = regexp.MustCompile(`[-_]?\d+$`)

func analyzeThreadDump(raw string) threadDumpAnalysis {
	an := threadDumpAnalysis{States: map[string]int{}, Pools: map[string]int{}, TopFrames: map[string]int{}}
	lines := strings.Split(raw, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if isThreadHeaderLine(line) {
			an.Total++
			if name := threadNameOf(line); name != "" {
				// 线程池里的线程叫 pool-1-thread-7、http-nio-8080-exec-23，去掉尾号才能聚成一类。
				an.Pools[threadPoolSuffixRe.ReplaceAllString(name, "")]++
			}
			// 状态在紧随其后的 "java.lang.Thread.State:" 行上。
			for j := i + 1; j < len(lines) && j <= i+3; j++ {
				if idx := strings.Index(lines[j], "java.lang.Thread.State:"); idx >= 0 {
					st := strings.Fields(strings.TrimSpace(lines[j][idx+len("java.lang.Thread.State:"):]))
					if len(st) > 0 {
						an.States[st[0]]++
					}
					break
				}
			}
			// 栈顶 = 该线程块里第一条 "at ..." 行。
			for j := i + 1; j < len(lines); j++ {
				t := strings.TrimSpace(lines[j])
				if t == "" || isThreadHeaderLine(lines[j]) {
					break
				}
				if strings.HasPrefix(t, "at ") {
					an.TopFrames[strings.TrimPrefix(t, "at ")]++
					break
				}
			}
		}
		if strings.Contains(line, "Found one Java-level deadlock") || strings.Contains(line, "Found ") && strings.Contains(line, "deadlock") {
			end := i + 60
			if end > len(lines) {
				end = len(lines)
			}
			an.Deadlock = strings.Join(lines[i:end], "\n")
		}
	}
	return an
}

// isThreadHeaderLine 判断一行是不是线程块的头。
//
// 光看"以引号开头"是不够的，而且这个疏漏会直接把结论带偏：jstack 末尾的**死锁段里也
// 会用引号列出线程名**（`"Thread-A":`、`"Thread-B":`），于是每检出一次死锁，线程总数
// 就凭空多几个——而线程总数正是"线程是否堆积"那条判断的分母。
// 真正的线程头必然带有 JVM 打出的标识（tid=/nid=/prio=/#序号）。
func isThreadHeaderLine(line string) bool {
	if !strings.HasPrefix(line, "\"") {
		return false
	}
	if strings.Index(line[1:], "\"") < 0 {
		return false
	}
	return strings.Contains(line, "tid=") || strings.Contains(line, "nid=") ||
		strings.Contains(line, "prio=") || strings.Contains(line, " #")
}

func threadNameOf(line string) string {
	if !strings.HasPrefix(line, "\"") {
		return ""
	}
	if e := strings.Index(line[1:], "\""); e >= 0 {
		return line[1 : 1+e]
	}
	return ""
}

type countKV struct {
	Key string
	N   int
}

func sortedCounts(m map[string]int) []countKV {
	out := make([]countKV, 0, len(m))
	for k, v := range m {
		out = append(out, countKV{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// ---------------------------------------------------------------------------
// java_heap_histo：堆对象直方图
// ---------------------------------------------------------------------------

// moduleJavaHeapHisto 抓堆内对象分布，用于判断"内存被什么占着"。
//
// **live 参数是有生产影响的**：jmap -histo:live 会先做一次 Full GC 再统计，在大堆上
// 是秒级 STW 停顿。默认因此关闭——只读巡检不该自己制造一次停顿。需要排除待回收对象的
// 干扰时才显式打开，并且应当在低峰执行。
func moduleJavaHeapHisto(args map[string]string) ([]byte, int) {
	jmap := javaTool("jmap")
	if jmap == "" {
		return javaToolMissing("jmap")
	}
	jvms := listJVMs()
	pid, err := resolveJVMPID(args, jvms)
	if err != nil {
		return []byte(err.Error()), 1
	}
	live := strings.TrimSpace(args["live"]) == "1"
	spec := "-histo"
	if live {
		spec = "-histo:live"
	}
	topN := atoiDefault(args["top"], 25)
	if topN < 5 {
		topN = 5
	}
	if topN > 100 {
		topN = 100
	}

	out, exit := runArgv([]string{jmap, spec, pid})
	raw := string(out)
	if exit != 0 && !strings.Contains(raw, "#instances") {
		return []byte(javaAttachHint("jmap", pid, raw)), 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# java_heap_histo: pid=%s live=%v top=%d\n", pid, live, topN)
	if live {
		b.WriteString("注意: live=1 已触发一次 Full GC（STW），大堆上可达秒级停顿。\n")
	} else {
		b.WriteString("说明: 未加 :live，统计含尚未回收的垃圾对象；判泄漏时可在低峰用 live=1 复采一次对比。\n")
	}
	kept := 0
	for _, line := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "num") || strings.HasPrefix(t, "---") || strings.HasPrefix(t, "Total") {
			b.WriteString(t + "\n")
			continue
		}
		if kept >= topN {
			continue
		}
		b.WriteString(t + "\n")
		kept++
	}
	return []byte(b.String()), 0
}

// ---------------------------------------------------------------------------
// java_jvm_info：运行时参数与版本
// ---------------------------------------------------------------------------

func moduleJavaJVMInfo(args map[string]string) ([]byte, int) {
	jinfo := javaTool("jinfo")
	if jinfo == "" {
		return javaToolMissing("jinfo")
	}
	jvms := listJVMs()
	pid, err := resolveJVMPID(args, jvms)
	if err != nil {
		return []byte(err.Error()), 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# java_jvm_info: pid=%s（只读，取运行时生效值而非启动参数）\n", pid)
	flags, exit := runArgv([]string{jinfo, "-flags", pid})
	if exit != 0 {
		return []byte(javaAttachHint("jinfo", pid, string(flags))), 1
	}
	b.WriteString("\n--- 生效的 JVM 参数 ---\n" + string(flags))
	if strings.TrimSpace(args["sysprops"]) == "1" {
		props, _ := runArgv([]string{jinfo, "-sysprops", pid})
		b.WriteString("\n--- 系统属性 ---\n" + truncRunes(string(props), 8000))
	}
	if java := javaTool("java"); java != "" {
		ver, _ := runArgv([]string{java, "-version"})
		b.WriteString("\n--- java -version（Agent 侧 PATH 上的 JDK，未必等于目标进程） ---\n" + string(ver))
	}
	return []byte(b.String()), 0
}

// ---------------------------------------------------------------------------
// java_exception_scan：异常分析
// ---------------------------------------------------------------------------

// javaLogCandidates 猜测应用日志位置。
//
// 巡检不能要求填参数才能跑——真到了排障现场，"这个应用日志在哪"本身就是要查的事。
// 所以先从 JVM 启动参数里取（Spring Boot 的 logging.file.name、logback/log4j 的
// 配置属性通常都以 -D 传入），再回退到几个约定位置。
func javaLogCandidates(args map[string]string, jvms []jvmProc) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			seen[p] = true
			out = append(out, p)
		}
	}
	if p := strings.TrimSpace(args["path"]); p != "" {
		add(p)
		return out
	}
	keys := []string{"-Dlogging.file.name=", "-Dlogging.file=", "-DLOG_FILE=", "-Dlog.file=", "-Dlog4j.logFile="}
	for _, j := range jvms {
		for _, tok := range strings.Fields(j.Args) {
			for _, k := range keys {
				if strings.HasPrefix(tok, k) {
					add(strings.TrimPrefix(tok, k))
				}
			}
		}
	}
	for _, p := range []string{
		"/var/log/tomcat/catalina.out", "/opt/tomcat/logs/catalina.out",
		"/var/log/app/app.log", "/var/log/java/app.log", "nohup.out", "logs/app.log",
	} {
		add(p)
	}
	return out
}

// moduleJavaExceptionScan 扫应用日志里的异常，按类型聚合。
//
// 直接 grep Exception 会得到几千行栈帧，看不出重点。这里只统计**异常首行**（形如
// `java.lang.XxxException: message`），按异常类聚合计数并记住最后一次出现的时间行，
// 于是"哪个异常在刷屏、最近还在不在发生"一眼可见。OOM / StackOverflow 单独提级——
// 它们不是业务异常，是进程级事故。
func moduleJavaExceptionScan(args map[string]string) ([]byte, int) {
	jvms := listJVMs()
	logs := javaLogCandidates(args, jvms)
	var b strings.Builder
	b.WriteString("# java_exception_scan: 应用日志异常聚合（只读）\n")
	if len(logs) == 0 {
		b.WriteString("未找到可扫描的日志文件。\n" +
			"已尝试：JVM 启动参数中的 -Dlogging.file.name / -Dlogging.file / -Dlog.file，以及常见约定路径。\n" +
			"处理：用 args.path 显式指定日志文件路径。\n")
		return []byte(b.String()), 1
	}
	tailBytes := int64(atoiDefault(args["tail_kb"], 2048)) * 1024 // 默认只看末尾 2MB
	if tailBytes < 64*1024 {
		tailBytes = 64 * 1024
	}
	if tailBytes > 64*1024*1024 {
		tailBytes = 64 * 1024 * 1024
	}

	exCount := map[string]int{}
	exLast := map[string]string{}
	fatal := map[string]int{}
	scanned := 0
	for _, lf := range logs {
		if scanned >= 3 { // 最多扫 3 个文件，避免巡检把机器 IO 打满
			break
		}
		scanned++
		text, err := readFileTail(lf, tailBytes)
		if err != nil {
			fmt.Fprintf(&b, "跳过 %s：%v\n", lf, err)
			continue
		}
		fmt.Fprintf(&b, "扫描: %s（末尾 %d KB）\n", lf, tailBytes/1024)
		for _, line := range strings.Split(text, "\n") {
			cls := javaExceptionClassOf(line)
			if cls == "" {
				continue
			}
			exCount[cls]++
			exLast[cls] = strings.TrimSpace(truncRunes(line, 300))
			switch {
			case strings.Contains(cls, "OutOfMemoryError"),
				strings.Contains(cls, "StackOverflowError"),
				strings.Contains(cls, "NoClassDefFoundError"):
				fatal[cls]++
			}
		}
	}

	if len(exCount) == 0 {
		b.WriteString("\n扫描范围内未发现异常堆栈。\n")
		return []byte(b.String()), 0
	}
	b.WriteString("\n异常聚合（按出现次数）:\n")
	for i, kv := range sortedCounts(exCount) {
		if i >= 15 {
			fmt.Fprintf(&b, "  …（另有 %d 类）\n", len(exCount)-i)
			break
		}
		fmt.Fprintf(&b, "  %5d 次  %s\n", kv.N, kv.Key)
		fmt.Fprintf(&b, "          最近一条: %s\n", exLast[kv.Key])
	}

	b.WriteString("\n判读:\n")
	found := false
	for _, kv := range sortedCounts(fatal) {
		found = true
		fmt.Fprintf(&b, "发现: %s 出现 %d 次——这是进程级错误而非业务异常，"+
			"JVM 此时状态已不可信，需结合 java_gc_stat / java_heap_histo 定位内存来源\n", kv.Key, kv.N)
	}
	if top := sortedCounts(exCount); len(top) > 0 && top[0].N >= 50 {
		found = true
		fmt.Fprintf(&b, "发现: %s 在扫描窗口内出现 %d 次，属于持续刷屏，优先处理\n", top[0].Key, top[0].N)
	}
	if !found {
		b.WriteString("异常量级正常，未见进程级错误。\n")
	}
	return []byte(b.String()), 0
}

// javaExceptionClassOf 从一行日志里认出异常首行的异常类名，非异常行返回 ""。
// 只认「包名.类名Exception/Error」后面跟冒号或行尾的形态，避免把栈帧行（at xxx）
// 和随口提到异常名的业务日志算进来。
func javaExceptionClassOf(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "at ") || strings.HasPrefix(t, "...") {
		return ""
	}
	for _, tok := range strings.Fields(t) {
		tok = strings.TrimSuffix(tok, ":")
		if !strings.HasSuffix(tok, "Exception") && !strings.HasSuffix(tok, "Error") {
			continue
		}
		if !strings.Contains(tok, ".") {
			continue
		}
		if strings.ContainsAny(tok, "()[]{}\"'") {
			continue
		}
		return tok
	}
	return ""
}

// readFileTail 读取文件末尾至多 n 字节。日志文件常有几个 GB，全量读进内存既慢又危险。
func readFileTail(path string, n int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := st.Size()
	off := int64(0)
	if size > n {
		off = size - n
	}
	if _, err := f.Seek(off, 0); err != nil {
		return "", err
	}
	buf := make([]byte, size-off)
	read, err := io.ReadFull(f, buf)
	if err != nil && read == 0 {
		return "", err
	}
	return string(buf[:read]), nil
}

// ---------------------------------------------------------------------------
// java_app_inspect：一站式 Java 应用巡检
// ---------------------------------------------------------------------------

// moduleJavaAppInspect 把上面几项串成一份可直接交给 AI 诊断的报告。
//
// 分开的模块适合"我已经知道要看 GC"；而巡检的前提恰恰是**还不知道问题在哪**，
// 所以需要一次把进程、参数、GC、线程、异常都过一遍，并把各段的「发现:」汇总到末尾。
// 末尾那份汇总就是 AI 诊断与人工判读共同的入口。
func moduleJavaAppInspect(args map[string]string) ([]byte, int) {
	jvms := listJVMs()
	var b strings.Builder
	b.WriteString("===== Java 应用巡检报告（只读）=====\n\n")

	b.WriteString("【1/5 进程与启动参数】\n")
	procOut, _ := moduleJavaProcesses(args)
	b.Write(procOut)

	if len(jvms) == 0 {
		b.WriteString("\n本机没有 JVM 进程，后续各项跳过。\n")
		return []byte(b.String()), 0
	}
	pid, err := resolveJVMPID(args, jvms)
	if err != nil {
		fmt.Fprintf(&b, "\n【目标进程未确定】%v\n后续 GC/线程/运行时参数各项跳过；请用 args.pid 或 args.name 指定后重跑。\n", err)
		b.WriteString("\n【5/5 异常日志】\n")
		exOut, _ := moduleJavaExceptionScan(args)
		b.Write(exOut)
		return []byte(b.String()), 0
	}
	sub := map[string]string{}
	for k, v := range args {
		sub[k] = v
	}
	sub["pid"] = pid

	b.WriteString("\n【2/5 运行时参数】\n")
	infoOut, _ := moduleJavaJVMInfo(sub)
	b.Write(infoOut)

	b.WriteString("\n【3/5 GC 健康度】\n")
	gcOut, _ := moduleJavaGCStat(sub)
	b.Write(gcOut)

	b.WriteString("\n【4/5 线程与阻塞】\n")
	tdOut, _ := moduleJavaThreadDump(sub)
	b.Write(tdOut)

	b.WriteString("\n【5/5 异常日志】\n")
	exOut, _ := moduleJavaExceptionScan(sub)
	b.Write(exOut)

	// 汇总各段的「发现:」——AI 诊断与人工判读都从这里起步。
	b.WriteString("\n===== 发现汇总 =====\n")
	n := 0
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "发现:") {
			n++
			fmt.Fprintf(&b, "%d. %s\n", n, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "发现:")))
		}
	}
	if n == 0 {
		b.WriteString("本次巡检未发现异常特征。\n")
	}
	return []byte(b.String()), 0
}
