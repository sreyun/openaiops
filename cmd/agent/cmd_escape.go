package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// buildCmdExeCmdLine assembles the literal command line for `cmd.exe /c <cmd>`.
//
// Kept in this build-tag-free file so Linux CI can assert its shape: the defect
// it fixes (Go's CRT-style `\"` escaping, which cmd.exe does not understand) is
// invisible on any platform where the tests actually run. See useRawCmdLine.
//
// Only argv[0] is quoted — the program path may contain spaces, while the
// remainder after /c must reach cmd.exe byte for byte.
func buildCmdExeCmdLine(exePath, command string) string {
	return `"` + strings.ReplaceAll(exePath, `"`, "") + `" /c ` + command
}

// runCmdEscaped is CombinedOutput plus the two isolation steps that keep
// playbook / exec children from dying with the Agent:
//
//   - Linux: move the process tree out of the Agent systemd cgroup
//   - Windows: CREATE_BREAKAWAY_FROM_JOB so SCM job teardown cannot reap them
func runCmdEscaped(cmd *exec.Cmd) ([]byte, error) {
	return runCmdEscapedCapped(cmd, 0)
}

// runCmdEscapedCapped 同上，但把捕获的输出限制在 max 字节内（<=0 表示不限）。
//
// 上限是必须的：只读巡检里随手一条 journalctl / kubectl get -A / docker logs 在繁忙
// 主机上都能吐出几百 MB，而这些字节先进 Agent 内存、再原样回传服务端。1G 内存的小
// 机器上，一个"只读"步骤足以把 Agent 撑爆——巡检把被巡检的机器搞出故障，是这类工具
// 最不该犯的错。截断处留一行明确标记，避免把截断当成"命令只输出了这些"。
func runCmdEscapedCapped(cmd *exec.Cmd, max int) ([]byte, error) {
	buf := &cappedBuffer{max: max}
	if cmd.Stdout == nil {
		cmd.Stdout = buf
	}
	if cmd.Stderr == nil {
		cmd.Stderr = buf
	}
	detachFromServiceJob(cmd)
	if err := cmd.Start(); err != nil {
		return buf.Bytes(), err
	}
	if cmd.Process != nil {
		escapeAgentCgroupTree(cmd.Process.Pid)
	}
	err := cmd.Wait()
	return buf.Bytes(), err
}

// cappedBuffer 是带上限的输出缓冲：超过 max 之后丢弃后续字节，并在末尾追加一行标记。
type cappedBuffer struct {
	buf      bytes.Buffer
	max      int
	dropped  int
	truncMsg bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.max <= 0 {
		return c.buf.Write(p)
	}
	room := c.max - c.buf.Len()
	if room > 0 {
		if len(p) <= room {
			return c.buf.Write(p)
		}
		_, _ = c.buf.Write(p[:room])
		c.dropped += len(p) - room
	} else {
		c.dropped += len(p)
	}
	// 对调用方仍然报告"全部写入"：命令不该因为我们不想要更多输出而收到 EPIPE 死掉。
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte {
	if c.dropped > 0 && !c.truncMsg {
		c.truncMsg = true
		fmt.Fprintf(&c.buf, "\n…（输出已截断：超出 %d 字节上限，另丢弃约 %d 字节。请用更小的范围参数重跑，例如 lines/tail/top）\n", c.max, c.dropped)
	}
	return c.buf.Bytes()
}
