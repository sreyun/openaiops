//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 这一组测试盯的是同一个承诺：运维在面板上按下「停止」，被巡检的机器上那条命令必须真的停。
// 它会从三个方向坏掉，所以分别钉住：
//
//  1. 已经停了还去起进程——「停止之后又动了一次这台机器」，是最难被发现的一种；
//  2. 进程起来了杀不掉——面板显示已停止，机器还在被 freshclam / compose up 压着；
//  3. 「停止」和「超时」混成一句话——运维会去查一个不存在的性能问题。
//
// 测试不依赖机器上真有 docker / freshclam：PATH 换成只有假二进制的临时目录，既保证在任何
// CI 机器上都跑得到真实的 exec 路径，也保证测试绝不会碰到真的容器运行时。

// stubPATH 把 PATH 换成只含假二进制的临时目录，返回该目录。PATH 是干净的（不追加系统目录），
// 这样测试绝无可能落到机器上真的 docker / freshclam 上去。
func stubPATH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	return dir
}

// sleepBin 在 PATH 被换掉之前解析出 sleep 的绝对路径——假二进制自己也要能睡。
func sleepBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("这台机器上没有 sleep: %v", err)
	}
	return p
}

func writeStub(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
	return p
}

// slowStub 造一个「秒回 compose version、其余一律睡死」的假二进制：pidFile 记下它的 PID，
// exec 掉自己是为了让 sleep 就是 Go 起的那个进程——杀掉子进程等于杀掉 sleep，测试才能
// 直接断言「那个进程没了」。
func slowStub(sleep, pidFile string, sleepSec int) string {
	return `if [ "$1" = "compose" ] && [ "$2" = "version" ]; then echo "stub compose v2"; exit 0; fi
echo $$ > ` + pidFile + `
exec ` + sleep + ` ` + strconv.Itoa(sleepSec) + `
`
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("假二进制没留下 PID（说明它压根没被执行）: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("PID 文件内容异常 %q: %v", raw, err)
	}
	return pid
}

type stopCase struct {
	name string
	run  func(context.Context) ([]byte, int)
}

func stopCases() []stopCase {
	return []stopCase{
		{"container_action", func(ctx context.Context) ([]byte, int) {
			return moduleContainerAction(ctx, map[string]string{"action": "start", "id": "c1"})
		}},
		{"container_logs", func(ctx context.Context) ([]byte, int) {
			return moduleContainerLogs(ctx, map[string]string{"id": "c1"})
		}},
		{"container_exec", func(ctx context.Context) ([]byte, int) {
			return moduleContainerExec(ctx, map[string]string{"id": "c1", "command": "true"})
		}},
		{"container_compose", func(ctx context.Context) ([]byte, int) {
			return moduleComposeAction(ctx, map[string]string{"action": "ps", "project": "p"})
		}},
		{"container_compose_ls", func(ctx context.Context) ([]byte, int) {
			return moduleComposeList(ctx, nil)
		}},
		{"clamav_update", func(ctx context.Context) ([]byte, int) {
			return moduleClamavUpdate(ctx, map[string]string{})
		}},
	}
}

// 已经停止的会话不该再在主机上起任何进程。
func TestModulesDoNotSpawnAfterStop(t *testing.T) {
	sleep := sleepBin(t)
	dir := stubPATH(t)
	pidFile := filepath.Join(dir, "pid")
	body := slowStub(sleep, pidFile, 60)
	writeStub(t, dir, "docker", body)
	writeStub(t, dir, "freshclam", body)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, c := range stopCases() {
		out, code := c.run(ctx)
		if code != moduleStopExit {
			t.Errorf("%s: exit=%d want %d（停止必须与 shell 步骤同码）out=%s", c.name, code, moduleStopExit, out)
		}
		if !strings.Contains(string(out), "剧本已停止") {
			t.Errorf("%s: 输出要说清是被停止的，得到: %s", c.name, out)
		}
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Fatal("会话已停止，却仍然在主机上起了进程")
	}
}

// 跑到一半按停止：进程要被杀掉，而且不能等它自己的超时（60s/45s/15min）。
func TestModulesStopKillsRunningProcess(t *testing.T) {
	for _, c := range stopCases() {
		t.Run(c.name, func(t *testing.T) {
			sleep := sleepBin(t)
			dir := stubPATH(t)
			pidFile := filepath.Join(dir, "pid")
			body := slowStub(sleep, pidFile, 60)
			writeStub(t, dir, "docker", body)
			writeStub(t, dir, "freshclam", body)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type res struct {
				out  []byte
				code int
			}
			done := make(chan res, 1)
			start := time.Now()
			go func() {
				out, code := c.run(ctx)
				done <- res{out, code}
			}()

			// 等进程真的起来，再按停止——否则测的就成了上一个用例。
			deadline := time.Now().Add(20 * time.Second)
			for {
				if _, err := os.Stat(pidFile); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("假二进制迟迟没被执行")
				}
				time.Sleep(20 * time.Millisecond)
			}
			pid := readPID(t, pidFile)
			cancel()

			select {
			case r := <-done:
				if el := time.Since(start); el > 15*time.Second {
					t.Errorf("停止用了 %v：说明没挂在会话 ctx 上，等的是模块自己的超时", el)
				}
				if r.code != moduleStopExit {
					t.Errorf("exit=%d want %d, out=%s", r.code, moduleStopExit, r.out)
				}
				if !strings.Contains(string(r.out), "剧本已停止") {
					t.Errorf("输出要说清是被停止的，得到: %s", r.out)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("按了停止，模块 30 秒都没返回")
			}

			for i := 0; i < 100 && processAlive(pid); i++ {
				time.Sleep(20 * time.Millisecond)
			}
			if processAlive(pid) {
				t.Fatalf("模块已返回，但主机上的进程 %d 还活着", pid)
			}
		})
	}
}

// 「命令自己超时」必须仍然读作超时：与「被停止」同码同文案，运维就分不清该不该改配置。
func TestModuleTimeoutIsNotReportedAsStop(t *testing.T) {
	sleep := sleepBin(t)
	dir := stubPATH(t)
	writeStub(t, dir, "docker", slowStub(sleep, filepath.Join(dir, "pid"), 60))

	start := time.Now()
	out, code := moduleContainerExec(context.Background(), map[string]string{
		"id": "c1", "command": "sleep 60", "timeout_sec": "5",
	})
	if el := time.Since(start); el > 25*time.Second {
		t.Fatalf("超时没生效，耗时 %v", el)
	}
	if code == moduleStopExit {
		t.Errorf("超时被报成了「已停止」: %s", out)
	}
	if code == 0 || !strings.Contains(string(out), "超时") {
		t.Errorf("exit=%d out=%s，期望明确的超时结论", code, out)
	}
}

// 分派层要把会话 ctx 交给模块。少了这一步，上面的用例全绿，线上照样停不掉——
// 这正是这次修复之前的状态。
func TestRunModuleCtxForwardsCancelToModule(t *testing.T) {
	sleep := sleepBin(t)
	dir := stubPATH(t)
	pidFile := filepath.Join(dir, "pid")
	writeStub(t, dir, "docker", slowStub(sleep, pidFile, 60))

	a := &Agent{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		_, code := a.runModuleCtx(ctx, `{"module":"container_action","args":{"action":"start","id":"c1"}}`)
		done <- code
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("分派没把 container_action 跑起来")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case code := <-done:
		if code != moduleStopExit {
			t.Errorf("exit=%d want %d", code, moduleStopExit)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("分派层没把 ctx 传下去：停止对模块步骤无效")
	}
}

// runArgv / runModuleCmds 是所有只读模块共用的执行器，同样钉住这两条。
func TestRunArgvStopKillsCommand(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	stub := writeStub(t, dir, "slow", "echo $$ > "+pidFile+"\nexec "+sleepBin(t)+" 60\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	start := time.Now()
	go func() {
		_, code := runArgv(ctx, []string{stub})
		done <- code
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stub 没被执行")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pid := readPID(t, pidFile)
	cancel()
	select {
	case code := <-done:
		if el := time.Since(start); el > 15*time.Second {
			t.Errorf("runArgv 停止耗时 %v，等的是自带的 5 分钟超时？", el)
		}
		if code != moduleStopExit {
			t.Errorf("exit=%d want %d", code, moduleStopExit)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("runArgv 没有响应取消")
	}
	for i := 0; i < 100 && processAlive(pid); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("runArgv 返回了，进程 %d 还活着", pid)
	}
}

func TestRunModuleCmdsSkipsRemainingAfterStop(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	stub := writeStub(t, dir, "touchit", "echo ran > "+marker+"\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, code := runModuleCmds(ctx, [][]string{{stub}, {stub}})
	if code != moduleStopExit {
		t.Errorf("exit=%d want %d, out=%s", code, moduleStopExit, out)
	}
	if !strings.Contains(string(out), "后续命令未执行") {
		t.Errorf("要写明后面的命令没跑，得到: %s", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("会话已停止，命令仍被执行")
	}
}

// WaitDelay 的兜底：子进程把 stdout 交给了后台孙进程时，cmd.Wait 会一直等那根管道关闭。
// 没有这道兜底，「停止」在这种命令上依旧是无界的——进程早没了，Agent 还在等 60 秒。
// 同时要保证兜底不改变结论：进程自己退出码是 0，就仍然是成功。
func TestRunArgvBoundedWhenChildLeaksStdout(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, "leaky", sleepBin(t)+" 60 &\necho done\nexit 0\n")

	start := time.Now()
	out, code := runArgv(context.Background(), []string{stub})
	el := time.Since(start)
	if el > 20*time.Second {
		t.Fatalf("等管道等了 %v：WaitDelay 没起作用", el)
	}
	if el < time.Second {
		t.Skip("这台机器上孙进程没有继承管道，用例不成立")
	}
	if code != 0 {
		t.Errorf("进程自己退出码是 0，不该被报成失败: exit=%d out=%s", code, out)
	}
	if !strings.Contains(string(out), "输出可能不完整") {
		t.Errorf("输出被截断要有交代，得到: %s", out)
	}
}

// moduleCombinedOutput 的等价断言，避免上面那条用例因平台差异 skip 掉就没人守这段逻辑。
func TestModuleCombinedOutputKeepsSuccessOnWaitDelay(t *testing.T) {
	dir := t.TempDir()
	stub := writeStub(t, dir, "leaky", sleepBin(t)+" 60 &\necho hi\nexit 0\n")
	cmd := exec.Command(stub)
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := moduleCombinedOutput(cmd)
	if err != nil {
		t.Fatalf("进程正常退出，不该返回错误: %v (out=%s)", err, out)
	}
	if !strings.Contains(string(out), "hi") {
		t.Errorf("输出丢了: %s", out)
	}
}
