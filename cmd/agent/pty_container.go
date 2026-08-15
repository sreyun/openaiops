//go:build linux || darwin

package main

import (
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// newContainerExecPTY starts `docker|podman exec -it <id> <shell>` attached to a real PTY
// so interactive tools (vim/top/bash) work inside the container.
func newContainerExecPTY(cli, containerID, shell string, cols, rows int) termShell {
	cli = strings.TrimSpace(cli)
	containerID = strings.TrimSpace(containerID)
	shell = strings.TrimSpace(shell)
	if cli == "" || containerID == "" {
		return nil
	}
	if shell == "" {
		shell = "sh"
	}
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 30
	}
	master, slavePath, err := ptyOpen()
	if err != nil {
		slog.Warn("container PTY open failed", "err", err)
		return nil
	}
	slave, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil
	}
	setWinsize(master.Fd(), cols, rows)

	cmd := exec.Command(cli, "exec", "-it", containerID, shell)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Env = buildShellEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		slog.Warn("container exec PTY 启动失败", "err", err, "cli", cli, "id", containerID)
		master.Close()
		slave.Close()
		return nil
	}
	slave.Close()
	return adoptUnixPTY(master, cmd)
}
