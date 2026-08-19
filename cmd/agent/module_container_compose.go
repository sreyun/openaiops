package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// composeCLI 探测可用的 compose 实现。
//
// 探测本身也要能被打断：原来这里是裸 exec.Command(...).Run()，既没有超时也不认取消——
// docker 客户端在守护进程僵住时可以挂上几分钟，于是「停止剧本」连一步都退不出来。
func composeCLI(ctx context.Context) (bin string, argsPrefix []string) {
	ctx = moduleCtx(ctx)
	probe := func(name string, args ...string) bool {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(pctx, name, args...)
		cmd.WaitDelay = 5 * time.Second
		_, err := moduleCombinedOutput(cmd)
		return err == nil
	}
	// Prefer "docker compose" (v2 plugin), then standalone docker-compose, then podman-compose.
	if _, err := exec.LookPath("docker"); err == nil {
		if probe("docker", "compose", "version") {
			return "docker", []string{"compose"}
		}
	}
	if _, err := exec.LookPath("docker-compose"); err == nil {
		return "docker-compose", nil
	}
	if _, err := exec.LookPath("podman-compose"); err == nil {
		return "podman-compose", nil
	}
	if _, err := exec.LookPath("podman"); err == nil {
		if probe("podman", "compose", "version") {
			return "podman", []string{"compose"}
		}
	}
	return "", nil
}

// moduleComposeList lists compose projects on the host.
func moduleComposeList(ctx context.Context, args map[string]string) ([]byte, int) {
	ctx = moduleCtx(ctx)
	// 停止检查要在探测 CLI 之前：ctx 已取消时探测命令根本起不来，落到 bin=="" 分支就会
	// 把一次「已停止」报成「这台机器没装 compose」。
	if moduleStopped(ctx) {
		return []byte("container_compose_ls " + moduleStopMsg), moduleStopExit
	}
	bin, prefix := composeCLI(ctx)
	if bin == "" {
		return []byte("skip: 未找到 docker compose / podman-compose，跳过 Compose 列表\n"), 0
	}
	// 列表查询原来也是裸 exec.Command：没超时、不认取消。30s 对一次 ls 足够宽。
	run := func(cmdArgs []string) ([]byte, error) {
		lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(lctx, bin, cmdArgs...)
		cmd.WaitDelay = 5 * time.Second
		return moduleCombinedOutput(cmd)
	}
	cmdArgs := append(append([]string{}, prefix...), "ls", "-a", "--format", "json")
	out, err := run(cmdArgs)
	if err != nil {
		if moduleStopped(ctx) {
			return []byte("container_compose_ls 已中止" + moduleStopMsg), moduleStopExit
		}
		// Older compose may not support ls --format json; fall back to plain ls.
		cmdArgs = append(append([]string{}, prefix...), "ls", "-a")
		out2, err2 := run(cmdArgs)
		if err2 != nil {
			if moduleStopped(ctx) {
				return []byte("container_compose_ls 已中止" + moduleStopMsg), moduleStopExit
			}
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			return []byte(msg), 1
		}
		return []byte(`{"projects_text":` + strconv.Quote(string(out2)) + `,"cli":` + strconv.Quote(bin) + `}`), 0
	}
	text := strings.TrimSpace(string(out))
	// docker compose ls --format json may emit NDJSON (one object per line).
	projects := []json.RawMessage{}
	if strings.HasPrefix(text, "[") {
		_ = json.Unmarshal([]byte(text), &projects)
	} else {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			projects = append(projects, json.RawMessage(line))
		}
	}
	resp, _ := json.Marshal(map[string]any{"cli": bin, "projects": projects, "raw": text})
	return resp, 0
}

// moduleComposeAction runs compose up/down/ps/logs/pull/restart for a project or file.
// Args: action, project|name, file (compose yaml path), services (optional), timeout_sec.
func moduleComposeAction(ctx context.Context, args map[string]string) ([]byte, int) {
	ctx = moduleCtx(ctx)
	// 同 moduleComposeList：先判停止，再探测 CLI。
	if moduleStopped(ctx) {
		return []byte("compose " + moduleStopMsg), moduleStopExit
	}
	bin, prefix := composeCLI(ctx)
	if bin == "" {
		return []byte("未找到 docker compose / docker-compose / podman-compose"), 1
	}
	action := strings.ToLower(strings.TrimSpace(args["action"]))
	switch action {
	case "up", "down", "ps", "logs", "pull", "restart", "stop", "start":
	default:
		return []byte("未知 action（up|down|ps|logs|pull|restart|stop|start）"), 1
	}
	project := strings.TrimSpace(args["project"])
	if project == "" {
		project = strings.TrimSpace(args["name"])
	}
	file := strings.TrimSpace(args["file"])
	if file != "" {
		if !filepath.IsAbs(file) {
			return []byte("file 必须是绝对路径"), 1
		}
		if st, err := os.Stat(file); err != nil || st.IsDir() {
			return []byte("compose 文件不存在: " + file), 1
		}
	}
	if project == "" && file == "" {
		return []byte("需要 project 或 file"), 1
	}

	cmdArgs := append([]string{}, prefix...)
	if file != "" {
		cmdArgs = append(cmdArgs, "-f", file)
	}
	if project != "" {
		cmdArgs = append(cmdArgs, "-p", project)
	}
	switch action {
	case "up":
		cmdArgs = append(cmdArgs, "up", "-d", "--remove-orphans")
	case "down":
		cmdArgs = append(cmdArgs, "down")
	case "ps":
		cmdArgs = append(cmdArgs, "ps", "-a")
	case "logs":
		tail := "200"
		if t := strings.TrimSpace(args["tail"]); t != "" {
			if n, err := strconv.Atoi(t); err == nil && n > 0 && n <= 5000 {
				tail = strconv.Itoa(n)
			}
		}
		cmdArgs = append(cmdArgs, "logs", "--tail", tail, "--no-color")
	case "pull":
		cmdArgs = append(cmdArgs, "pull")
	case "restart":
		cmdArgs = append(cmdArgs, "restart")
	case "stop":
		cmdArgs = append(cmdArgs, "stop")
	case "start":
		cmdArgs = append(cmdArgs, "start")
	}
	if svc := strings.TrimSpace(args["services"]); svc != "" {
		for _, s := range strings.Fields(strings.ReplaceAll(svc, ",", " ")) {
			if s != "" {
				cmdArgs = append(cmdArgs, s)
			}
		}
	}

	timeout := 180 * time.Second
	if t := strings.TrimSpace(args["timeout_sec"]); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n >= 30 && n <= 600 {
			timeout = time.Duration(n) * time.Second
		}
	}
	if moduleStopped(ctx) {
		return []byte("compose " + action + " " + moduleStopMsg), moduleStopExit
	}
	// 原来这里是 goroutine + time.After + Process.Kill()：超时能杀进程，但「停止剧本」杀不动，
	// 而且超时分支一走，那个 goroutine 还挂在 CombinedOutput 上直到进程真的收尸。改成挂在会话
	// ctx 派生出的 cctx 上，超时与停止走同一条杀进程路径，WaitDelay 兜住管道不肯关的情况。
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, cmdArgs...)
	cmd.WaitDelay = 5 * time.Second
	out, err := moduleCombinedOutput(cmd)
	if moduleStopped(ctx) {
		return []byte(fmt.Sprintf("compose %s 已中止%s", action, moduleStopMsg)), moduleStopExit
	}
	if cctx.Err() == context.DeadlineExceeded {
		return []byte(fmt.Sprintf("compose %s 超时", action)), 1
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return []byte(msg), 1
	}
	if len(out) > 512*1024 {
		out = append(out[:512*1024], []byte("\n…[truncated]")...)
	}
	return out, 0
}
