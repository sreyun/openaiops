//go:build linux

package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Linux / 麒麟 / UOS desktop capture:
//   - X11: prefer continuous ffmpeg x11grab H.264; JPEG fallbacks via x11grab/import/scrot
//   - Wayland (含麒麟 Wayland 会话): prefer grim → raw H.264 pipe（避免每帧 spawn 解码 PNG）
// Input via xdotool (X11/XWayland), ydotool / wtype (Wayland).

type linuxCapture struct {
	display      string
	w, h         int
	cropX, cropY int
	monID        int
	outputName   string // xrandr / grim output name for multi-monitor crop
	wayland      bool
	// origMode 是会话开始时的 xrandr 模式（"1920x1080"）；改过分辨率才非空，
	// 会话收尾时据此还原（见 desktop_resolution_linux.go）。
	origMode string
}

func openDeskCapture() (deskCapture, error) {
	display := os.Getenv("DISPLAY")
	wayland := linuxWaylandSession()
	if display == "" && !wayland {
		display = ":0"
	}
	w, h := 1920, 1080
	if display != "" {
		if out, err := exec.Command("xdpyinfo", "-display", display).Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "dimensions:") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						parts := strings.Split(fields[1], "x")
						if len(parts) == 2 {
							if ww, e1 := strconv.Atoi(parts[0]); e1 == nil {
								w = ww
							}
							if hh, e2 := strconv.Atoi(parts[1]); e2 == nil {
								h = hh
							}
						}
					}
				}
			}
		}
	}
	c := &linuxCapture{display: display, w: w, h: h, wayland: wayland}
	if _, err := c.Capture(); err != nil {
		return nil, fmt.Errorf("%s", linuxCaptureFailHint(display, wayland, err))
	}
	if wayland {
		slog.Info("Linux 远程桌面：Wayland 会话", "distro", linuxDistroID(), "grim", lookPathOK("grim"), "ffmpeg", ffmpegAvailable())
	} else {
		slog.Info("Linux 远程桌面：X11 会话", "display", display, "distro", linuxDistroID(), "ffmpeg", ffmpegAvailable())
	}
	return c, nil
}

func lookPathOK(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// linuxDistroID reads ID= from /etc/os-release (kylin / uos / ubuntu / …).
func linuxDistroID() string {
	raw, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ID=") {
			v := strings.TrimPrefix(line, "ID=")
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func linuxIsKylinFamily() bool {
	id := strings.ToLower(linuxDistroID())
	switch id {
	case "kylin", "uos", "deepin", "openkylin":
		return true
	}
	// Some builds use ID_LIKE
	raw, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return false
	}
	low := strings.ToLower(string(raw))
	return strings.Contains(low, "kylin") || strings.Contains(low, "uniontech")
}

func linuxCaptureFailHint(display string, wayland bool, err error) string {
	hint := fmt.Sprintf("抓屏失败（DISPLAY=%q WAYLAND=%v distro=%s）: %v", display, wayland, linuxDistroID(), err)
	if wayland {
		hint += "；Wayland 请安装 grim（及可选 slurp），并确保 Agent 在图形用户会话中运行"
		if linuxIsKylinFamily() {
			hint += "；麒麟/UOS 可用：sudo apt install grim wl-clipboard ydotool 或从软件商店安装对应包"
		}
	} else {
		hint += "；X11 请安装 ffmpeg（推荐）或 scrot/imagemagick，并确保 DISPLAY/XAUTHORITY 有效"
		if linuxIsKylinFamily() {
			hint += "；麒麟/UOS 可用：sudo apt install ffmpeg xdotool xclip"
		}
	}
	hint += "；勿在纯 SSH/无头环境启动远程桌面"
	return hint
}

func (c *linuxCapture) Size() (int, int) { return c.w, c.h }
func (c *linuxCapture) Close() error     { return nil }

func (c *linuxCapture) Capture() (image.Image, error) {
	var lastErr error
	try := func(fn func() (image.Image, error)) (image.Image, error) {
		img, err := fn()
		if err != nil {
			lastErr = err
			return nil, err
		}
		img = c.cropFullDesktopIfNeeded(img)
		b := img.Bounds()
		if b.Dx() > 0 && b.Dy() > 0 {
			c.w, c.h = b.Dx(), b.Dy()
		}
		return img, nil
	}

	if c.wayland {
		if _, err := exec.LookPath("grim"); err == nil {
			if img, err := try(c.captureGrim); err == nil {
				return img, nil
			}
		}
	}

	if c.display != "" {
		if _, err := exec.LookPath("ffmpeg"); err == nil {
			if img, err := try(c.captureFFmpeg); err == nil {
				return img, nil
			}
		}
		if _, err := exec.LookPath("import"); err == nil {
			if img, err := try(c.captureImport); err == nil {
				return img, nil
			}
		}
		if _, err := exec.LookPath("scrot"); err == nil {
			if img, err := try(c.captureScrot); err == nil {
				return img, nil
			}
		}
		if _, err := exec.LookPath("gnome-screenshot"); err == nil {
			if img, err := try(c.captureGnome); err == nil {
				return img, nil
			}
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no usable screen capture tool")
}

// cropFullDesktopIfNeeded cuts a monitor rectangle out of a full-desktop shot
// (import/scrot/gnome fallbacks ignore crop). ffmpeg/grim already capture the
// selected region, so their frames typically match c.w×c.h and are left alone.
func (c *linuxCapture) cropFullDesktopIfNeeded(img image.Image) image.Image {
	if img == nil || c.w <= 0 || c.h <= 0 {
		return img
	}
	if c.cropX == 0 && c.cropY == 0 && c.monID <= 1 {
		return img
	}
	b := img.Bounds()
	if b.Dx() <= c.w && b.Dy() <= c.h {
		return img
	}
	r := image.Rect(b.Min.X+c.cropX, b.Min.Y+c.cropY, b.Min.X+c.cropX+c.w, b.Min.Y+c.cropY+c.h)
	r = r.Intersect(b)
	if r.Empty() {
		return img
	}
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	if s, ok := img.(subImager); ok {
		return s.SubImage(r)
	}
	return img
}

func (c *linuxCapture) captureFFmpeg() (image.Image, error) {
	grab := c.display
	if c.cropX != 0 || c.cropY != 0 {
		grab = fmt.Sprintf("%s+%d,%d", c.display, c.cropX, c.cropY)
	}
	// Single-frame MJPEG to stdout — keep draw_mouse and avoid CombinedOutput
	// mixing stderr diagnostics into the image bytes.
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "x11grab", "-draw_mouse", "1",
		"-framerate", "30",
		"-video_size", fmt.Sprintf("%dx%d", c.w, c.h),
		"-i", grab,
		"-frames:v", "1",
		"-f", "image2pipe", "-vcodec", "mjpeg", "-q:v", "3", "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg x11grab: %v (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return jpeg.Decode(bytes.NewReader(stdout.Bytes()))
}

func (c *linuxCapture) captureImport() (image.Image, error) {
	cmd := exec.Command("import", "-display", c.display, "-window", "root", "png:-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("import: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return png.Decode(bytes.NewReader(out))
}

func (c *linuxCapture) captureScrot() (image.Image, error) {
	tmp, err := os.CreateTemp("", "aiops-desk-scrot-*.png")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)
	cmd := exec.Command("scrot", "-o", path)
	cmd.Env = append(os.Environ(), "DISPLAY="+c.display)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("scrot: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return png.Decode(bytes.NewReader(raw))
}

func (c *linuxCapture) captureGnome() (image.Image, error) {
	tmp, err := os.CreateTemp("", "aiops-desk-gnome-*.png")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)
	cmd := exec.Command("gnome-screenshot", "-f", path)
	cmd.Env = append(os.Environ(), "DISPLAY="+c.display)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("gnome-screenshot: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return png.Decode(bytes.NewReader(raw))
}

func (c *linuxCapture) captureGrim() (image.Image, error) {
	// JPEG is much cheaper to decode than PNG under high FPS / unlock typing.
	args := []string{"-t", "jpeg"}
	if c.outputName != "" {
		args = append(args, "-o", c.outputName)
	}
	args = append(args, "-")
	cmd := exec.Command("grim", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Older grim without -t jpeg — fall back to PNG stdout.
		return c.captureGrimPNG()
	}
	if img, err := jpeg.Decode(bytes.NewReader(stdout.Bytes())); err == nil {
		return img, nil
	}
	return c.captureGrimPNG()
}

func (c *linuxCapture) captureGrimPNG() (image.Image, error) {
	args := []string{"-"}
	if c.outputName != "" {
		args = []string{"-o", c.outputName, "-"}
	}
	cmd := exec.Command("grim", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("grim: %v (%s)", err, strings.TrimSpace(stderr.String()))
	}
	if img, err := png.Decode(bytes.NewReader(stdout.Bytes())); err == nil {
		return img, nil
	}
	return jpeg.Decode(bytes.NewReader(stdout.Bytes()))
}

type linuxInput struct {
	display   string
	mouseTool string // xdotool | ydotool | ""
	keyTool   string // xdotool | wtype | ydotool | ""
	originX   int
	originY   int
}

func openDeskInput() (deskInput, error) {
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}
	wayland := linuxWaylandSession()
	hasXdo := lookPathOK("xdotool")
	hasYdo := lookPathOK("ydotool")
	hasWtype := lookPathOK("wtype")

	in := &linuxInput{display: display}
	switch {
	case hasXdo && !wayland:
		in.mouseTool, in.keyTool = "xdotool", "xdotool"
	case hasXdo && wayland:
		// XWayland often still works for xdotool.
		in.mouseTool, in.keyTool = "xdotool", "xdotool"
	case hasYdo && hasWtype:
		in.mouseTool, in.keyTool = "ydotool", "wtype"
	case hasYdo:
		in.mouseTool, in.keyTool = "ydotool", "ydotool"
	case hasWtype:
		in.keyTool = "wtype"
		slog.Warn("仅找到 wtype：可注入键盘，鼠标不可用；建议安装 xdotool 或 ydotool")
	case hasXdo:
		in.mouseTool, in.keyTool = "xdotool", "xdotool"
	default:
		hint := "no input tool found (install xdotool for X11, or ydotool/wtype for Wayland)"
		if linuxIsKylinFamily() {
			hint = "未找到键鼠注入工具；麒麟/UOS 请安装 xdotool（X11）或 ydotool/wtype（Wayland）：sudo apt install xdotool ydotool"
		}
		return nil, fmt.Errorf("%s", hint)
	}
	if in.mouseTool == "ydotool" || in.keyTool == "ydotool" {
		// ydotoold must be running; surface a clear hint once instead of silent failures.
		if err := exec.Command("ydotool", "mousemove", "-x", "0", "-y", "0").Run(); err != nil {
			slog.Warn("ydotool 不可用（通常需要 ydotoold 守护进程在运行）", "err", err)
		}
	}
	return in, nil
}

func (i *linuxInput) Close() error { return nil }

func (i *linuxInput) SetOrigin(x, y int) { i.originX, i.originY = x, y }

func (i *linuxInput) env() []string {
	return append(os.Environ(), "DISPLAY="+i.display)
}

func (i *linuxInput) runXdo(args ...string) error {
	cmd := exec.Command("xdotool", args...)
	cmd.Env = i.env()
	return cmd.Run()
}

func (i *linuxInput) MouseMove(x, y int) error {
	ax, ay := i.originX+x, i.originY+y
	switch i.mouseTool {
	case "xdotool":
		return i.runXdo("mousemove", "--sync", strconv.Itoa(ax), strconv.Itoa(ay))
	case "ydotool":
		return exec.Command("ydotool", "mousemove", "--absolute", "-x", strconv.Itoa(ax), "-y", strconv.Itoa(ay)).Run()
	default:
		return nil
	}
}

func (i *linuxInput) MouseButton(button int, down bool) error {
	// Protocol: 1=left, 2=right, 3=middle. xdotool: 1=left, 2=middle, 3=right.
	b := 1
	switch button {
	case 2:
		b = 3
	case 3:
		b = 2
	}
	switch i.mouseTool {
	case "xdotool":
		if down {
			return i.runXdo("mousedown", strconv.Itoa(b))
		}
		return i.runXdo("mouseup", strconv.Itoa(b))
	case "ydotool":
		yb := 0
		switch button {
		case 2:
			yb = 1 // right
		case 3:
			yb = 2 // middle
		}
		v := "0"
		if down {
			v = "1"
		}
		return exec.Command("ydotool", "click", fmt.Sprintf("%d:%s", yb, v)).Run()
	default:
		return nil
	}
}

func (i *linuxInput) MouseWheel(delta int) error {
	btn := "4"
	if delta < 0 {
		btn = "5"
	}
	n := delta
	if n < 0 {
		n = -n
	}
	if n == 0 {
		n = 1
	}
	if n > 5 {
		n = 5
	}
	switch i.mouseTool {
	case "xdotool":
		for k := 0; k < n; k++ {
			if err := i.runXdo("click", btn); err != nil {
				return err
			}
		}
	case "ydotool":
		dy := -40
		if delta < 0 {
			dy = 40
		}
		for k := 0; k < n; k++ {
			_ = exec.Command("ydotool", "mousemove", "-x", "0", "-y", strconv.Itoa(dy)).Run()
		}
	}
	return nil
}

func (i *linuxInput) Key(vk int, down bool) error {
	name := linuxVKName(vk)
	if name == "" {
		return nil
	}
	switch i.keyTool {
	case "xdotool":
		if down {
			return i.runXdo("keydown", name)
		}
		return i.runXdo("keyup", name)
	case "wtype":
		if !down {
			return nil
		}
		if len(name) == 1 {
			return exec.Command("wtype", name).Run()
		}
		return exec.Command("wtype", "-k", name).Run()
	case "ydotool":
		if code := linuxEVKey(vk); code > 0 {
			state := "0"
			if down {
				state = "1"
			}
			return exec.Command("ydotool", "key", fmt.Sprintf("%d:%s", code, state)).Run()
		}
		if !down {
			return nil
		}
		if len(name) == 1 {
			return exec.Command("ydotool", "type", name).Run()
		}
		return nil
	}
	return nil
}

func (i *linuxInput) SendCAD() error {
	// Closest X11 equivalent: Ctrl+Alt+Backspace (often disabled; still best effort).
	return deskPlayChord(i, "ctrl_alt_bksp")
}

func (i *linuxInput) TypeText(text string) error {
	switch i.keyTool {
	case "xdotool":
		return i.runXdo("type", "--clearmodifiers", "--", text)
	case "wtype":
		return exec.Command("wtype", "--", text).Run()
	case "ydotool":
		return exec.Command("ydotool", "type", text).Run()
	default:
		return deskTypeTextViaKeys(i, text)
	}
}

func (i *linuxInput) DeskInputMeta() deskInputMeta {
	return deskInputMeta{
		Desktop:        i.display,
		InputDesktopOK: i.keyTool != "",
		CAD:            false,
		TypeText:       i.keyTool != "",
		LockHint:       deskDefaultLockHint(),
	}
}

// linuxEVKey maps a subset of VKs to Linux input event keycodes for ydotool.
func linuxEVKey(vk int) int {
	switch vk {
	case 0x08:
		return 14
	case 0x09:
		return 15
	case 0x0D:
		return 28
	case 0x1B:
		return 1
	case 0x20:
		return 57
	case 0x25:
		return 105
	case 0x26:
		return 103
	case 0x27:
		return 106
	case 0x28:
		return 108
	case 0x2E:
		return 111
	case 0x10, 0xA0:
		return 42 // KEY_LEFTSHIFT
	case 0xA1:
		return 54 // KEY_RIGHTSHIFT
	case 0x11, 0xA2:
		return 29 // KEY_LEFTCTRL
	case 0xA3:
		return 97 // KEY_RIGHTCTRL
	case 0x12, 0xA4:
		return 56 // KEY_LEFTALT
	case 0xA5:
		return 100 // KEY_RIGHTALT
	case 0x5B:
		return 125 // KEY_LEFTMETA
	case 0x5C:
		return 126 // KEY_RIGHTMETA
	}
	if vk >= 'A' && vk <= 'Z' {
		// Approximate QWERTY positions — good enough for letters.
		row := []int{
			30, 48, 46, 32, 18, 33, 34, 35, 23, 36, 37, 38, 50,
			49, 24, 25, 16, 19, 31, 20, 22, 47, 17, 45, 21, 44,
		}
		return row[vk-'A']
	}
	return 0
}

func deskGOOS() string { return "linux" }

func deskH264Usable() bool {
	if !ffmpegAvailable() {
		return false
	}
	// Continuous x11grab (X11 / XWayland).
	if os.Getenv("DISPLAY") != "" {
		return true
	}
	// Pure Wayland: grim frames → ffmpeg rawvideo H.264.
	if linuxWaylandSession() && lookPathOK("grim") {
		return true
	}
	return false
}

func deskPreferredCodec() string {
	if deskH264Usable() {
		return "h264"
	}
	return ""
}

// deskNeedsRawH264: Wayland (含麒麟) 用 grim 喂帧；纯无 DISPLAY 同理。
// X11 直连 x11grab，无需 raw。
func deskNeedsRawH264() bool {
	if !ffmpegAvailable() {
		return false
	}
	if linuxWaylandSession() && lookPathOK("grim") {
		return true
	}
	return os.Getenv("DISPLAY") == "" && lookPathOK("grim")
}

func deskLegacyCaptureHost() bool { return false }
func deskAVFScreenIndex() int     { return -1 }

func linuxVKName(vk int) string {
	switch vk {
	case 0x08:
		return "BackSpace"
	case 0x09:
		return "Tab"
	case 0x0D:
		return "Return"
	case 0x1B:
		return "Escape"
	case 0x20:
		return "space"
	case 0x21:
		return "Page_Up"
	case 0x22:
		return "Page_Down"
	case 0x23:
		return "End"
	case 0x24:
		return "Home"
	case 0x25:
		return "Left"
	case 0x26:
		return "Up"
	case 0x27:
		return "Right"
	case 0x28:
		return "Down"
	case 0x2D:
		return "Insert"
	case 0x2E:
		return "Delete"
	case 0x10, 0xA0:
		return "Shift_L"
	case 0xA1:
		return "Shift_R"
	case 0x11, 0xA2:
		return "Control_L"
	case 0xA3:
		return "Control_R"
	case 0x12, 0xA4:
		return "Alt_L"
	case 0xA5:
		return "Alt_R"
	case 0x14:
		return "Caps_Lock"
	case 0x5B:
		return "Super_L"
	case 0x5C:
		return "Super_R"
	case 0x5D:
		return "Menu"
	case 0x70:
		return "F1"
	case 0x71:
		return "F2"
	case 0x72:
		return "F3"
	case 0x73:
		return "F4"
	case 0x74:
		return "F5"
	case 0x75:
		return "F6"
	case 0x76:
		return "F7"
	case 0x77:
		return "F8"
	case 0x78:
		return "F9"
	case 0x79:
		return "F10"
	case 0x7A:
		return "F11"
	case 0x7B:
		return "F12"
	case 0xBD:
		return "minus"
	case 0xBB:
		return "equal"
	case 0xDB:
		return "bracketleft"
	case 0xDD:
		return "bracketright"
	case 0xDC:
		return "backslash"
	case 0xBA:
		return "semicolon"
	case 0xDE:
		return "apostrophe"
	case 0xC0:
		return "grave"
	case 0xBC:
		return "comma"
	case 0xBE:
		return "period"
	case 0xBF:
		return "slash"
	case 0x60, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69:
		return "KP_" + string(rune('0'+vk-0x60))
	case 0x6A:
		return "KP_Multiply"
	case 0x6B:
		return "KP_Add"
	case 0x6D:
		return "KP_Subtract"
	case 0x6E:
		return "KP_Decimal"
	case 0x6F:
		return "KP_Divide"
	}
	if vk >= 'A' && vk <= 'Z' {
		return string(rune(vk + 32))
	}
	if vk >= '0' && vk <= '9' {
		return string(rune(vk))
	}
	return ""
}
