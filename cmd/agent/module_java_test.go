package main

import (
	"strings"
	"testing"
)

// jstat 的表头随 JDK 版本变化（JDK8 与 JDK11+ 的列集合并不相同），所以解析必须按
// **列名**取值。按固定下标取会在换一个 JDK 版本后静默取到错误的列——数字看着正常，
// 结论全错，而且没有任何报错。
func TestParseJstatGCUtilByColumnName(t *testing.T) {
	jdk8 := `  S0     S1     E      O      M     CCS    YGC     YGCT    FGC    FGCT     GCT   
  0.00  12.50  35.20  61.10  95.30  92.10    120    3.450     2    0.880    4.330
  0.00  25.00  70.40  61.30  95.31  92.10    121    3.470     2    0.880    4.350
`
	rows := parseJstatGCUtil(jdk8)
	if len(rows) != 2 {
		t.Fatalf("应解析出 2 行采样，得到 %d", len(rows))
	}
	if rows[0].O != 61.10 || rows[1].O != 61.30 {
		t.Fatalf("老年代列取错: %v / %v", rows[0].O, rows[1].O)
	}
	if rows[0].YGC != 120 || rows[1].YGC != 121 {
		t.Fatalf("YGC 列取错: %d / %d", rows[0].YGC, rows[1].YGC)
	}
	if rows[1].FGCT != 0.880 {
		t.Fatalf("FGCT 列取错: %v", rows[1].FGCT)
	}

	// 列顺序不同（这里把 M/CCS 挪到前面）也必须取对——正是按名取列要防的情况。
	shuffled := `  E      O      YGC    YGCT   FGC    FGCT   M   
 10.00  20.00     5    0.100     1    0.200  50.00
 11.00  21.00     6    0.120     1    0.200  50.10
`
	rows2 := parseJstatGCUtil(shuffled)
	if len(rows2) != 2 || rows2[1].O != 21.00 || rows2[1].M != 50.10 || rows2[1].YGC != 6 {
		t.Fatalf("换列序后解析错误: %+v", rows2)
	}
}

func TestParseJstatGCUtilIgnoresGarbage(t *testing.T) {
	if rows := parseJstatGCUtil("connection refused\n"); len(rows) != 0 {
		t.Fatalf("非表格输出不应产生采样行，得到 %+v", rows)
	}
}

const sampleDump = `2026-08-19 10:00:00
Full thread dump OpenJDK 64-Bit Server VM:

"http-nio-8080-exec-1" #21 daemon prio=5 tid=0x01 nid=0x1 waiting for monitor entry
   java.lang.Thread.State: BLOCKED (on object monitor)
	at com.example.OrderService.place(OrderService.java:42)
	at com.example.Controller.post(Controller.java:11)

"http-nio-8080-exec-2" #22 daemon prio=5 tid=0x02 nid=0x2 waiting for monitor entry
   java.lang.Thread.State: BLOCKED (on object monitor)
	at com.example.OrderService.place(OrderService.java:42)

"pool-3-thread-9" #40 prio=5 tid=0x03 nid=0x3 waiting on condition
   java.lang.Thread.State: WAITING (parking)
	at jdk.internal.misc.Unsafe.park(Native Method)

"main" #1 prio=5 tid=0x04 nid=0x4 runnable
   java.lang.Thread.State: RUNNABLE
	at java.net.SocketInputStream.socketRead0(Native Method)

Found one Java-level deadlock:
=============================
"Thread-A":
  waiting to lock monitor 0x05, which is held by "Thread-B"
`

func TestAnalyzeThreadDump(t *testing.T) {
	an := analyzeThreadDump(sampleDump)
	if an.Total != 4 {
		t.Fatalf("线程总数应为 4，得到 %d", an.Total)
	}
	if an.States["BLOCKED"] != 2 || an.States["WAITING"] != 1 || an.States["RUNNABLE"] != 1 {
		t.Fatalf("状态分布错误: %+v", an.States)
	}
	// 栈顶聚类：两个线程卡在同一行，这就是阻塞点。
	if an.TopFrames["com.example.OrderService.place(OrderService.java:42)"] != 2 {
		t.Fatalf("栈顶聚类错误: %+v", an.TopFrames)
	}
	// 线程池按名字前缀聚合：exec-1/exec-2 必须归成一类，否则每个线程各算一个"池"。
	if an.Pools["http-nio-8080-exec"] != 2 {
		t.Fatalf("线程池聚合错误: %+v", an.Pools)
	}
	if an.Deadlock == "" || !strings.Contains(an.Deadlock, "Thread-B") {
		t.Fatalf("死锁段未捕获: %q", an.Deadlock)
	}
}

// 死锁是确定性故障、不会自愈，漏报的代价是让人继续在别处找原因。
func TestAnalyzeThreadDumpNoDeadlockWhenAbsent(t *testing.T) {
	clean := `"main" #1 prio=5 nid=0x1 runnable
   java.lang.Thread.State: RUNNABLE
	at com.example.App.main(App.java:1)
`
	an := analyzeThreadDump(clean)
	if an.Deadlock != "" {
		t.Fatalf("无死锁时不应报死锁: %q", an.Deadlock)
	}
	if an.Total != 1 || an.States["RUNNABLE"] != 1 {
		t.Fatalf("解析错误: total=%d states=%+v", an.Total, an.States)
	}
}

func TestJavaExceptionClassOf(t *testing.T) {
	yes := map[string]string{
		"2026-08-19 10:00:00 ERROR c.e.S - java.lang.NullPointerException: name is null": "java.lang.NullPointerException",
		"java.lang.OutOfMemoryError: Java heap space":                                    "java.lang.OutOfMemoryError",
		"Caused by: java.sql.SQLTimeoutException":                                        "java.sql.SQLTimeoutException",
	}
	for line, want := range yes {
		if got := javaExceptionClassOf(line); got != want {
			t.Fatalf("javaExceptionClassOf(%q)=%q want %q", line, got, want)
		}
	}
	// 栈帧行与业务日志不能被当成异常首行，否则一次异常会被计成几十次。
	no := []string{
		"	at com.example.Foo.bar(Foo.java:10)",
		"... 42 more",
		"INFO 处理完成，无 Exception",
		"",
		"ExceptionHandler registered",
	}
	for _, line := range no {
		if got := javaExceptionClassOf(line); got != "" {
			t.Fatalf("javaExceptionClassOf(%q) 不该匹配，得到 %q", line, got)
		}
	}
}

func TestParseJVMFlags(t *testing.T) {
	f := parseJVMFlags("-Xms2g -Xmx4g -XX:MaxMetaspaceSize=256m -XX:+UseG1GC -XX:+HeapDumpOnOutOfMemoryError -XX:HeapDumpPath=/data/dump -Dfoo=bar")
	if f.Xms != "2g" || f.Xmx != "4g" || f.MaxMeta != "256m" {
		t.Fatalf("堆参数解析错误: %+v", f)
	}
	if f.Collector != "G1GC" {
		t.Fatalf("收集器解析错误: %q", f.Collector)
	}
	if !f.HeapDumpOnOOM || f.HeapDumpPath != "/data/dump" {
		t.Fatalf("OOM 转储解析错误: %+v", f)
	}

	// 没配 -Xmx / 没开 OOM 转储是巡检要报的两个「发现」，解析必须如实反映。
	bare := parseJVMFlags("-jar app.jar")
	if bare.Xmx != "" || bare.HeapDumpOnOOM {
		t.Fatalf("空配置解析错误: %+v", bare)
	}
}

func TestJavaMainFromArgv(t *testing.T) {
	cases := map[string]string{
		"java -Xmx1g -jar /opt/app/app.jar --spring.profiles.active=prod": "/opt/app/app.jar",
		"java -cp /opt/lib/* -Xmx1g com.example.Main arg1":                "com.example.Main",
	}
	for cmdline, want := range cases {
		if got := javaMainFromArgv(strings.Fields(cmdline)); got != want {
			t.Fatalf("javaMainFromArgv(%q)=%q want %q", cmdline, got, want)
		}
	}
}

// 目标进程解析不出来时，必须把候选列表回给调用方——一句"未找到"会让人无从下手。
func TestResolveJVMPID(t *testing.T) {
	jvms := []jvmProc{
		{PID: "100", Main: "/opt/order/order.jar"},
		{PID: "200", Main: "/opt/pay/pay.jar"},
	}
	if pid, err := resolveJVMPID(map[string]string{"pid": "100"}, jvms); err != nil || pid != "100" {
		t.Fatalf("显式 pid 解析失败: %q %v", pid, err)
	}
	if pid, err := resolveJVMPID(map[string]string{"name": "pay"}, jvms); err != nil || pid != "200" {
		t.Fatalf("按名匹配失败: %q %v", pid, err)
	}
	if _, err := resolveJVMPID(map[string]string{}, jvms); err == nil {
		t.Fatal("多个 JVM 且未指定时应报错")
	} else if !strings.Contains(err.Error(), "100") || !strings.Contains(err.Error(), "200") {
		t.Fatalf("报错里应列出候选进程，得到: %v", err)
	}
	single := []jvmProc{{PID: "300", Main: "app.jar"}}
	if pid, err := resolveJVMPID(map[string]string{}, single); err != nil || pid != "300" {
		t.Fatalf("只有一个 JVM 时应直接选它: %q %v", pid, err)
	}
	if _, err := resolveJVMPID(map[string]string{"pid": "abc"}, single); err == nil {
		t.Fatal("非数字 pid 应报错")
	}
	if _, err := resolveJVMPID(map[string]string{}, nil); err == nil {
		t.Fatal("没有 JVM 时应报错")
	}
}
