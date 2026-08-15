//go:build linux || darwin

package main

import (
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// Unix pseudo-terminal (openpty) backing for the remote terminal — a real TTY so
// colours, line editing, job control and full-screen programs (vim/top) work.
// Pure syscall (no cgo, no third-party): open /dev/ptmx, unlock + name the slave
// (per-OS ioctls live in pty_linux.go / pty_darwin.go), then spawn the login
// shell with the slave as its controlling terminal.

type winsize struct {
	rows, cols, xpix, ypix uint16
}

func ioctl(fd, req, arg uintptr) syscall.Errno {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg)
	return e
}

func setWinsize(fd uintptr, cols, rows int) {
	ws := winsize{rows: uint16(rows), cols: uint16(cols)}
	ioctl(fd, ptyWinszReq, uintptr(unsafe.Pointer(&ws)))
}

type unixPTY struct {
	master *os.File
	cmd    *exec.Cmd
	holder *exec.Cmd // keeps the PTY master open if the Agent process dies
}

// newPTY opens a pty pair and starts the shell attached to it. Returns nil on any
// failure so the caller falls back to piped stdio.
func newPTY(cols, rows int) termShell {
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 30
	}
	master, slavePath, err := ptyOpen()
	if err != nil {
		return nil
	}
	slave, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil
	}
	setWinsize(master.Fd(), cols, rows)

	// Build a proper shell environment — systemd/minimal contexts often lack
	// HOME/USER/SHELL, which causes "cd: HOME not set" and broken ~ expansion.
	// shellPath() never returns nologin (service accounts); -l sources profile.
	sh := shellPath()
	env := buildShellEnv()
	dir := interactiveShellDir()
	// Prefer login+interactive; fall back to interactive-only when -l is rejected
	// (some busybox ash builds) so the remote terminal still comes up.
	// On Linux root: wrap with nsenter into PID 1 mount ns so systemd
	// ProtectSystem cannot leave /etc read-only for the interactive shell.
	var cmd *exec.Cmd
	var usedNsenter bool
	for _, shArgs := range [][]string{{"-l", "-i"}, {"-i"}, {}} {
		name, args, viaNs := linuxInteractiveShellInvocation(sh, shArgs, dir)
		c := exec.Command(name, args...)
		c.Stdin, c.Stdout, c.Stderr = slave, slave, slave
		c.Env = env
		// When nsenter --wd= is set, Dir is unused by the child cwd; keep for non-nsenter.
		if !viaNs {
			c.Dir = dir
		}
		// Ctty=0: slave is cmd.Stdin — required on Linux when Setctty is set.
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
		if err := c.Start(); err != nil {
			slog.Warn("PTY shell 启动失败，尝试降级参数", "err", err, "bin", name, "args", args, "dir", dir, "nsenter", viaNs)
			continue
		}
		cmd = c
		usedNsenter = viaNs
		slog.Info("PTY shell 已启动", "bin", name, "shell", sh, "args", shArgs, "pid", c.Process.Pid, "dir", dir, "nsenter", viaNs)
		break
	}
	// nsenter itself may fail (no --wd support); fall back to a plain shell so the
	// terminal still opens (may remain sandboxed — banner warns when /etc is RO).
	if cmd == nil {
		for _, shArgs := range [][]string{{"-l", "-i"}, {"-i"}, {}} {
			name, args, _ := linuxInteractiveShellInvocationPlain(sh, shArgs)
			c := exec.Command(name, args...)
			c.Stdin, c.Stdout, c.Stderr = slave, slave, slave
			c.Env = env
			c.Dir = dir
			c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if err := c.Start(); err != nil {
				slog.Warn("PTY plain shell 启动失败", "err", err, "bin", name, "args", args)
				continue
			}
			cmd = c
			usedNsenter = false
			slog.Info("PTY shell 已启动（无 nsenter 降级）", "bin", name, "shell", sh, "pid", c.Process.Pid)
			break
		}
	}
	_ = usedNsenter
	if cmd == nil {
		master.Close()
		slave.Close()
		return nil
	}
	slave.Close() // the child owns the slave now; the parent only needs the master
	return adoptUnixPTY(master, cmd)
}

// adoptUnixPTY moves the shell tree out of the Agent cgroup and attaches a
// holder process that keeps the PTY master open. Without the holder, Agent
// death closes the last master fd and the kernel SIGHUPs the foreground job
// (xjar → Java) even when KillMode=process left those processes alive.
func adoptUnixPTY(master *os.File, cmd *exec.Cmd) *unixPTY {
	if cmd != nil && cmd.Process != nil {
		escapeAgentCgroupTree(cmd.Process.Pid)
	}
	return &unixPTY{master: master, cmd: cmd, holder: attachPTYHolder(master)}
}

// attachPTYHolder starts a tiny setsid process that inherits a dup of master
// and then sleeps. It is itself cgroup-escaped so a mixed/control-group unit
// stop cannot take the holder down and hang up the session.
func attachPTYHolder(master *os.File) *exec.Cmd {
	if master == nil {
		return nil
	}
	for _, argv := range [][]string{
		{"sleep", "infinity"},
		{"sleep", "2147483647"},
		{"tail", "-f", "/dev/null"},
	} {
		c := exec.Command(argv[0], argv[1:]...)
		c.Stdin = nil
		c.Stdout = nil
		c.Stderr = nil
		c.ExtraFiles = []*os.File{master}
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err != nil {
			continue
		}
		if c.Process != nil {
			escapeAgentCgroup(c.Process.Pid)
		}
		return c
	}
	slog.Warn("PTY holder 启动失败，Agent 退出时前台作业可能收到 SIGHUP")
	return nil
}

// ensureUTF8 is a no-op on Linux/macOS: the terminal already uses UTF-8 by
// default (or the exec session sets LANG=en_US.UTF-8). No conversion needed.
func ensureUTF8(b []byte) []byte { return b }

// ensureUTF8Hold is a no-op on Unix (already UTF-8); never holds trailing bytes.
func ensureUTF8Hold(data []byte) (out, hold []byte) { return data, nil }

func (u *unixPTY) Read(b []byte) (int, error)  { return u.master.Read(b) }
func (u *unixPTY) Write(b []byte) (int, error) { return u.master.Write(b) }
func (u *unixPTY) Resize(cols, rows int) error { setWinsize(u.master.Fd(), cols, rows); return nil }
func (u *unixPTY) Wait() error                 { return u.cmd.Wait() }
func (u *unixPTY) Close() error {
	if u.cmd != nil && u.cmd.Process != nil {
		_ = u.cmd.Process.Kill()
	}
	if u.holder != nil && u.holder.Process != nil {
		_ = u.holder.Process.Kill()
		go func() { _ = u.holder.Wait() }()
	}
	if u.master != nil {
		return u.master.Close()
	}
	return nil
}
