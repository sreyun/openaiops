//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func (c *linuxCapture) Monitors() []deskMonitorInfo {
	if list := linuxMonitorsXrandr(); len(list) > 0 {
		return list
	}
	if list := linuxMonitorsWlrRandr(); len(list) > 0 {
		return list
	}
	if list := linuxMonitorsGrim(); len(list) > 0 {
		return list
	}
	w, h := c.Size()
	return []deskMonitorInfo{{ID: 1, Name: "default", Width: w, Height: h, Primary: true}}
}

func linuxMonitorsXrandr() []deskMonitorInfo {
	// xrandr --listmonitors → " 0: +*DP-1 1920/508x1080/286+0+0  DP-1"
	out, err := exec.Command("xrandr", "--listmonitors").Output()
	if err != nil {
		return nil
	}
	var list []deskMonitorInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Monitors:") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		idStr := strings.TrimSuffix(parts[0], ":")
		id, _ := strconv.Atoi(idStr)
		id++ // 1-based
		geom := parts[2]
		primary := strings.Contains(parts[1], "*")
		name := parts[len(parts)-1]
		w, h, x, y := parseXrandrGeom(geom)
		if w == 0 {
			continue
		}
		list = append(list, deskMonitorInfo{ID: id, Name: name, Width: w, Height: h, X: x, Y: y, Primary: primary})
	}
	return list
}

// linuxMonitorsWlrRandr parses `wlr-randr` (wlroots / 部分麒麟 Wayland).
func linuxMonitorsWlrRandr() []deskMonitorInfo {
	out, err := exec.Command("wlr-randr").Output()
	if err != nil {
		return nil
	}
	var list []deskMonitorInfo
	var cur *deskMonitorInfo
	flush := func() {
		if cur != nil && cur.Width > 0 && cur.Height > 0 {
			list = append(list, *cur)
		}
		cur = nil
	}
	id := 1
	for _, line := range strings.Split(string(out), "\n") {
		raw := line
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Output name lines are unindented: "HDMI-A-1 \"…\""
		if len(raw) > 0 && raw[0] != ' ' && raw[0] != '\t' && !strings.Contains(line, "px,") {
			flush()
			name := strings.Fields(line)[0]
			cur = &deskMonitorInfo{ID: id, Name: name, Primary: strings.Contains(line, "(current)") || id == 1}
			id++
			continue
		}
		if cur == nil {
			continue
		}
		// "  1920x1080 px, …" or "current 1920x1080@…"
		if strings.Contains(line, "x") && (strings.Contains(line, "px") || strings.Contains(line, "@") || strings.Contains(line, "current")) {
			for _, tok := range strings.Fields(line) {
				tok = strings.TrimSuffix(tok, ",")
				if !strings.Contains(tok, "x") {
					continue
				}
				wh := strings.SplitN(strings.Split(tok, "@")[0], "x", 2)
				if len(wh) != 2 {
					continue
				}
				w, e1 := strconv.Atoi(wh[0])
				h, e2 := strconv.Atoi(wh[1])
				if e1 == nil && e2 == nil && w > 0 && h > 0 {
					cur.Width, cur.Height = w, h
					break
				}
			}
		}
		if strings.Contains(line, "position") || strings.HasPrefix(line, "Position:") {
			// "Position: 1920,0" or "  position: 1920,0"
			rest := line
			if i := strings.Index(strings.ToLower(line), "position"); i >= 0 {
				rest = line[i:]
			}
			rest = strings.TrimPrefix(strings.ToLower(rest), "position:")
			rest = strings.TrimSpace(rest)
			parts := strings.Split(rest, ",")
			if len(parts) >= 2 {
				x, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
				y, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
				cur.X, cur.Y = x, y
			}
		}
		if strings.Contains(strings.ToLower(line), "enabled") && strings.Contains(strings.ToLower(line), "yes") {
			cur.Primary = cur.Primary || cur.ID == 1
		}
	}
	flush()
	if len(list) > 0 {
		list[0].Primary = true
	}
	return list
}

// linuxMonitorsGrim uses `grim -g` listing via swaymsg/hyprctl when available.
func linuxMonitorsGrim() []deskMonitorInfo {
	// hyprctl monitors -j is common on newer 麒麟/社区 Wayland spins.
	if out, err := exec.Command("hyprctl", "monitors", "-j").Output(); err == nil {
		if list := parseHyprMonitorsJSON(string(out)); len(list) > 0 {
			return list
		}
	}
	return nil
}

func parseHyprMonitorsJSON(s string) []deskMonitorInfo {
	type m struct {
		Name   string `json:"name"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
		X      int    `json:"x"`
		Y      int    `json:"y"`
	}
	var arr []m
	if err := json.Unmarshal([]byte(s), &arr); err != nil || len(arr) == 0 {
		return nil
	}
	out := make([]deskMonitorInfo, 0, len(arr))
	for i, v := range arr {
		if v.Width <= 0 || v.Height <= 0 {
			continue
		}
		out = append(out, deskMonitorInfo{
			ID: i + 1, Name: v.Name, Width: v.Width, Height: v.Height,
			X: v.X, Y: v.Y, Primary: i == 0,
		})
	}
	return out
}

func parseXrandrGeom(s string) (w, h, x, y int) {
	// 1920/508x1080/286+0+0
	main := strings.Split(s, "+")
	wh := strings.Split(main[0], "x")
	if len(wh) < 2 {
		return
	}
	wPart := strings.Split(wh[0], "/")[0]
	hPart := strings.Split(wh[1], "/")[0]
	w, _ = strconv.Atoi(wPart)
	h, _ = strconv.Atoi(hPart)
	if len(main) >= 3 {
		x, _ = strconv.Atoi(main[1])
		y, _ = strconv.Atoi(main[2])
	}
	return
}

func (c *linuxCapture) SetMonitor(id int) error {
	for _, m := range c.Monitors() {
		if m.ID == id {
			c.cropX, c.cropY = m.X, m.Y
			c.w, c.h = m.Width, m.Height
			c.monID = id
			c.outputName = m.Name
			return nil
		}
	}
	return fmt.Errorf("monitor %d not found", id)
}

func (c *linuxCapture) Origin() (x, y int) { return c.cropX, c.cropY }

func linuxWaylandSession() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("XDG_SESSION_TYPE") == "wayland"
}

func deskClipboardSupported() bool {
	if linuxWaylandSession() {
		if _, err := exec.LookPath("wl-paste"); err == nil {
			return true
		}
		if _, err := exec.LookPath("wl-copy"); err == nil {
			return true
		}
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return true
	}
	if _, err := exec.LookPath("xsel"); err == nil {
		return true
	}
	return false
}

func deskClipboardGet() (string, error) {
	if linuxWaylandSession() {
		if _, err := exec.LookPath("wl-paste"); err == nil {
			out, err := exec.Command("wl-paste", "-n").Output()
			return string(out), err
		}
	}
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.Command("xclip", "-selection", "clipboard", "-o")
		cmd.Env = append(os.Environ(), "DISPLAY="+display)
		out, err := cmd.Output()
		return string(out), err
	}
	if _, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.Command("xsel", "--clipboard", "--output")
		cmd.Env = append(os.Environ(), "DISPLAY="+display)
		out, err := cmd.Output()
		return string(out), err
	}
	return "", fmt.Errorf("need wl-clipboard (Wayland) or xclip/xsel (X11)")
}

func deskClipboardSet(text string) error {
	if linuxWaylandSession() {
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd := exec.Command("wl-copy")
			cmd.Stdin = strings.NewReader(text)
			return cmd.Run()
		}
	}
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		cmd := exec.Command("xclip", "-selection", "clipboard")
		cmd.Env = append(os.Environ(), "DISPLAY="+display)
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	if _, err := exec.LookPath("xsel"); err == nil {
		cmd := exec.Command("xsel", "--clipboard", "--input")
		cmd.Env = append(os.Environ(), "DISPLAY="+display)
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	return fmt.Errorf("need wl-clipboard (Wayland) or xclip/xsel (X11)")
}
