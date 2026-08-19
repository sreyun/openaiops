package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// java_exception_scan 读的是**调用方指定的任意文件**，和 file_head 是同一类能力，
// 就必须过同一道敏感路径闸门。原来它完全没过：一个标注"只读"的巡检模块可以拿
// args.path 指到私钥或 Agent 自己的 config.yaml（安装 token + relay_secret）。
func TestJavaExceptionScanRefusesSensitivePath(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "id_rsa")
	if err := os.WriteFile(key, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, exit := moduleJavaExceptionScan(context.Background(), map[string]string{"path": key})
	if exit == 0 {
		t.Fatalf("敏感路径必须被拒绝，得到 exit=0：%s", out)
	}
	// 拒绝要看得见：静默跳过会让调用方以为"日志路径填错了"，去查一个不存在的问题。
	if !strings.Contains(string(out), "拒绝读取敏感路径") {
		t.Fatalf("拒绝理由必须写明是敏感路径拦截，得到：%s", out)
	}
}

// 自动发现出来的路径同样要过闸门：-Dlogging.file.name 是进程启动参数，谁能起进程谁就能写它。
func TestJavaLogCandidatesSkipsDeniedAutoDiscovered(t *testing.T) {
	dir := t.TempDir()
	pem := filepath.Join(dir, "server.pem")
	if err := os.WriteFile(pem, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	jvms := []jvmProc{{PID: "1", Main: "app.jar", Args: "-Dlogging.file.name=" + pem}}
	if got := javaLogCandidates(map[string]string{}, jvms); len(got) != 0 {
		t.Fatalf("自动发现的敏感路径必须被剔除，得到 %v", got)
	}

	ok := filepath.Join(dir, "app.log")
	if err := os.WriteFile(ok, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	jvms = []jvmProc{{PID: "1", Main: "app.jar", Args: "-Dlogging.file.name=" + ok}}
	if got := javaLogCandidates(map[string]string{}, jvms); len(got) != 1 || got[0] != ok {
		t.Fatalf("正常日志路径不该被误伤，得到 %v", got)
	}
}

// 字面判定认不出 /tmp/x -> /etc/shadow。这道闸是给"以 root 运行的 Agent"设的，
// 宿主机上的普通用户造一个软链就能借它读走只有 root 能读的文件。
func TestAgentDeniedPathFollowsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 建软链需要额外权限")
	}
	dir := t.TempDir()
	secret := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(secret, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "app.log")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("无法创建软链: %v", err)
	}
	if !agentDeniedPath(link) {
		t.Fatal("指向私钥的软链必须被拦截")
	}

	plain := filepath.Join(dir, "real.log")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.log")
	if err := os.Symlink(plain, alias); err != nil {
		t.Skipf("无法创建软链: %v", err)
	}
	if agentDeniedPath(alias) {
		t.Fatal("指向普通日志的软链不该被误伤")
	}
}

// force=1 走 ptrace，会就地挂起目标进程且不校验对方是不是 JVM——pid 必须先出现在
// 本机的 JVM 清单里，否则一次"只读巡检"就能把一个数据库进程按停。
func TestJavaForceDumpRequiresDiscoveredJVM(t *testing.T) {
	jvms := []jvmProc{{PID: "4321", Main: "app.jar"}}
	if !jvmPIDDiscovered("4321", jvms) {
		t.Fatal("清单内的 pid 应被认可")
	}
	if jvmPIDDiscovered("1", jvms) {
		t.Fatal("清单外的 pid 不得被认可")
	}
	msg := javaForceRefusal("1", jvms)
	if !strings.Contains(msg, "ptrace") || !strings.Contains(msg, "4321") {
		t.Fatalf("拒绝理由要写明原因并给出候选进程，得到：%s", msg)
	}
}
