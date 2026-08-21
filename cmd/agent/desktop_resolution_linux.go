//go:build linux

package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Linux（X11）侧的远程分辨率控制：走 xrandr。
//
// Wayland 下没有通用的对外接口（各合成器各一套），所以只在 X11 上支持；
// 不支持时明确报错，绝不假装成功——UI 是按"有没有报模式列表"来决定要不要显示这个入口的。

var xrandrModeRe = regexp.MustCompile(`^\s{2,}(\d+)x(\d+)\s`)

// xrandrAvailable 报告当前会话能不能用 xrandr 改分辨率。
func (c *linuxCapture) xrandrAvailable() bool {
	if c == nil || c.wayland || strings.TrimSpace(c.display) == "" {
		return false
	}
	_, err := exec.LookPath("xrandr")
	return err == nil
}

func (c *linuxCapture) xrandrEnv() []string {
	env := os.Environ()
	if c.display != "" {
		env = append(env, "DISPLAY="+c.display)
	}
	return env
}

func (c *linuxCapture) xrandrQuery() (string, error) {
	cmd := exec.Command("xrandr", "--query")
	cmd.Env = c.xrandrEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("xrandr --query 失败：%w", err)
	}
	return string(out), nil
}

// xrandrOutput 取要操作的输出名：优先当前抓屏用的那个，否则第一个 connected。
func (c *linuxCapture) xrandrOutput(query string) string {
	if strings.TrimSpace(c.outputName) != "" {
		return c.outputName
	}
	for _, line := range strings.Split(query, "\n") {
		if strings.Contains(line, " connected") {
			if f := strings.Fields(line); len(f) > 0 {
				return f[0]
			}
		}
	}
	return ""
}

func (c *linuxCapture) Modes() []deskDisplayMode {
	if !c.xrandrAvailable() {
		return nil
	}
	query, err := c.xrandrQuery()
	if err != nil {
		return nil
	}
	out := c.xrandrOutput(query)
	if out == "" {
		return nil
	}
	var modes []deskDisplayMode
	inTarget := false
	for _, line := range strings.Split(query, "\n") {
		if !strings.HasPrefix(line, " ") {
			// 输出行（"HDMI-1 connected ..."）：切换"当前是不是目标输出"。
			inTarget = strings.HasPrefix(line, out+" ")
			continue
		}
		if !inTarget {
			continue
		}
		m := xrandrModeRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		modes = append(modes, deskDisplayMode{W: w, H: h})
	}
	return deskSortModes(modes)
}

func (c *linuxCapture) SetMode(w, h int) error {
	if !c.xrandrAvailable() {
		return fmt.Errorf("当前会话不支持改分辨率（需要 X11 + xrandr；Wayland 无通用接口）")
	}
	query, err := c.xrandrQuery()
	if err != nil {
		return err
	}
	out := c.xrandrOutput(query)
	if out == "" {
		return fmt.Errorf("找不到已连接的显示输出")
	}
	if c.origMode == "" {
		if cur := xrandrCurrentMode(query, out); cur != "" {
			c.origMode = cur
		}
	}
	mode := fmt.Sprintf("%dx%d", w, h)
	cmd := exec.Command("xrandr", "--output", out, "--mode", mode)
	cmd.Env = c.xrandrEnv()
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("xrandr 切换到 %s 失败：%s", mode, strings.TrimSpace(string(b)))
	}
	slog.Info("远程桌面已切换分辨率", "output", out, "mode", mode)
	return nil
}

func (c *linuxCapture) Restore() error {
	if c == nil || c.origMode == "" || !c.xrandrAvailable() {
		return nil
	}
	mode := c.origMode
	c.origMode = ""
	query, err := c.xrandrQuery()
	if err != nil {
		return err
	}
	out := c.xrandrOutput(query)
	if out == "" {
		return nil
	}
	cmd := exec.Command("xrandr", "--output", out, "--mode", mode)
	cmd.Env = c.xrandrEnv()
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("恢复分辨率 %s 失败：%s", mode, strings.TrimSpace(string(b)))
	}
	slog.Info("远程桌面已恢复原分辨率", "output", out, "mode", mode)
	return nil
}

// xrandrCurrentMode 从 --query 输出里读出某个输出当前生效的模式（带 * 标记的那一行）。
func xrandrCurrentMode(query, output string) string {
	inTarget := false
	for _, line := range strings.Split(query, "\n") {
		if !strings.HasPrefix(line, " ") {
			inTarget = strings.HasPrefix(line, output+" ")
			continue
		}
		if !inTarget || !strings.Contains(line, "*") {
			continue
		}
		if m := xrandrModeRe.FindStringSubmatch(line); m != nil {
			return m[1] + "x" + m[2]
		}
	}
	return ""
}
