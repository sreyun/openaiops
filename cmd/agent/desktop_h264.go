package main

import (
	"fmt"
	"image"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Video encode path via ffmpeg (fragmented MP4 over WebSocket).
//
// Why H.264 remains the default (not H.265-only):
//   - Browser MSE for HEVC is fragmented (Safari OK; Chrome needs HW+OS; Firefox often no).
//   - Soft libx265 is far slower than libx264 ultrafast — hurts unlock/typing latency.
//   - Hardware HEVC (VideoToolbox/NVENC/QSV) is offered when both sides support it.
//
// Negotiation: client sends client_codecs; agent picks h265|h264|jpeg.

type h264Pipe struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stdin  io.WriteCloser
	once   sync.Once
	rawW   int
	rawH   int
	codec  string // h264 | h265
	enc    string // ffmpeg encoder name
	qual   int
	fps    int
}

func (p *h264Pipe) Codec() string   { return p.codec }
func (p *h264Pipe) Encoder() string { return p.enc }
func (p *h264Pipe) Quality() int    { return p.qual }
func (p *h264Pipe) IsRaw() bool     { return p != nil && p.stdin != nil }

func ffmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

type deskEncoderKind struct {
	Name     string // ffmpeg -c:v name
	Codec    string // h264 | h265
	Hardware bool
}

var (
	deskEncOnce   sync.Once
	deskEncH264   []deskEncoderKind
	deskEncH265   []deskEncoderKind
	deskEncProbed bool
)

func deskProbeEncoders() {
	deskEncOnce.Do(func() {
		deskEncProbed = true
		if !ffmpegAvailable() {
			return
		}
		encOut, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").CombinedOutput()
		if err != nil {
			return
		}
		have := map[string]bool{}
		for _, line := range strings.Split(string(encOut), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				have[fields[1]] = true
			}
		}
		candidates := []deskEncoderKind{
			{Name: "hevc_videotoolbox", Codec: "h265", Hardware: true},
			{Name: "hevc_nvenc", Codec: "h265", Hardware: true},
			{Name: "hevc_qsv", Codec: "h265", Hardware: true},
			{Name: "hevc_amf", Codec: "h265", Hardware: true},
			{Name: "hevc_vaapi", Codec: "h265", Hardware: true},
			{Name: "h264_videotoolbox", Codec: "h264", Hardware: true},
			{Name: "h264_nvenc", Codec: "h264", Hardware: true},
			{Name: "h264_qsv", Codec: "h264", Hardware: true},
			{Name: "h264_amf", Codec: "h264", Hardware: true},
			{Name: "h264_vaapi", Codec: "h264", Hardware: true},
			{Name: "libx264", Codec: "h264", Hardware: false},
			{Name: "libx265", Codec: "h265", Hardware: false},
		}
		for _, c := range candidates {
			if !have[c.Name] {
				continue
			}
			if !deskEncoderSmokeOK(c.Name) {
				continue
			}
			if c.Codec == "h265" {
				deskEncH265 = append(deskEncH265, c)
			} else {
				deskEncH264 = append(deskEncH264, c)
			}
		}
		names := make([]string, 0, len(deskEncH264)+len(deskEncH265))
		for _, e := range deskEncH264 {
			names = append(names, e.Name)
		}
		for _, e := range deskEncH265 {
			names = append(names, e.Name)
		}
		if len(names) > 0 {
			slog.Info("远程桌面视频编码器已探测", "encoders", strings.Join(names, ","))
		}
	})
}

func ffmpegEncoderExists(name string) bool {
	deskProbeEncoders()
	for _, e := range deskEncH264 {
		if e.Name == name {
			return true
		}
	}
	for _, e := range deskEncH265 {
		if e.Name == name {
			return true
		}
	}
	return false
}

func deskEncoderSmokeOK(name string) bool {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=0.2",
		"-frames:v", "1",
	}
	args = append(args, deskEncoderArgs(name, 23, 10)...)
	args = append(args, "-f", "null", "-")
	cmd := exec.Command("ffmpeg", args...)
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(4 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return false
	}
}

func deskH264Encoders() []deskEncoderKind {
	deskProbeEncoders()
	return deskEncH264
}

func deskH265Encoders() []deskEncoderKind {
	deskProbeEncoders()
	return deskEncH265
}

func deskHEVCUsable() bool {
	return len(deskH265Encoders()) > 0
}

func deskPickEncoder(codec string, allowSoftHEVC bool) (deskEncoderKind, error) {
	codec = strings.ToLower(strings.TrimSpace(codec))
	switch codec {
	case "h265", "hevc":
		list := deskH265Encoders()
		for _, e := range list {
			if e.Hardware {
				return e, nil
			}
		}
		if allowSoftHEVC {
			for _, e := range list {
				return e, nil
			}
		}
		return deskEncoderKind{}, fmt.Errorf("no usable H.265 encoder")
	default:
		list := deskH264Encoders()
		if len(list) == 0 {
			return deskEncoderKind{}, fmt.Errorf("no usable H.264 encoder")
		}
		return list[0], nil
	}
}

// deskNegotiateVideoCodec chooses h265|h264|"" (jpeg) from client request + capabilities.
func deskNegotiateVideoCodec(want string, clientCodecs []string) string {
	want = strings.ToLower(strings.TrimSpace(want))
	client := map[string]bool{}
	for _, c := range clientCodecs {
		client[strings.ToLower(strings.TrimSpace(c))] = true
	}
	clientEmpty := len(client) == 0

	supports := func(c string) bool {
		if clientEmpty {
			return c == "h264"
		}
		return client[c] || (c == "h265" && (client["hevc"] || client["h265"]))
	}

	h264OK := len(deskH264Encoders()) > 0
	h265OK := deskHEVCUsable()

	switch want {
	case "h265", "hevc":
		if h265OK && supports("h265") {
			if _, err := deskPickEncoder("h265", true); err == nil {
				return "h265"
			}
		}
		if h264OK && supports("h264") {
			return "h264"
		}
	case "h264":
		if h264OK && supports("h264") {
			return "h264"
		}
	case "jpeg", "":
		return ""
	}

	if h265OK && supports("h265") {
		if e, err := deskPickEncoder("h265", false); err == nil && e.Hardware {
			return "h265"
		}
	}
	if h264OK && supports("h264") {
		return "h264"
	}
	return ""
}

func deskPreferredVideoCodec() string {
	if e, err := deskPickEncoder("h265", false); err == nil && e.Hardware {
		return "h265"
	}
	if len(deskH264Encoders()) > 0 {
		return "h264"
	}
	if deskHEVCUsable() {
		return "h265"
	}
	return ""
}

func deskEncoderArgs(encName string, quality, fps int) []string {
	if quality < 1 {
		quality = 80
	}
	if quality > 100 {
		quality = 100
	}
	if fps < 1 {
		fps = 15
	}
	crf := deskQualityToCRF(quality)
	bitrate := deskQualityToBitrateK(quality)
	g := strconv.Itoa(fps)

	switch encName {
	case "libx264":
		preset := "ultrafast"
		if quality >= 88 {
			preset = "veryfast"
		} else if quality >= 78 {
			preset = "superfast"
		}
		return []string{
			"-c:v", "libx264",
			"-preset", preset,
			"-tune", "zerolatency",
			"-pix_fmt", "yuv420p",
			"-g", g,
			"-bf", "0",
			"-x264-params", fmt.Sprintf("keyint=%d:min-keyint=%d:scenecut=0:rc-lookahead=0:sync-lookahead=0:bframes=0:ref=1:sliced-threads=1:aq-mode=2", fps, fps),
			"-crf", strconv.Itoa(crf),
		}
	case "libx265":
		return []string{
			"-c:v", "libx265",
			"-preset", "ultrafast",
			"-pix_fmt", "yuv420p",
			"-g", g,
			"-bf", "0",
			"-x265-params", fmt.Sprintf("keyint=%d:min-keyint=%d:bframes=0:rc-lookahead=0:repeat-headers=1:scenecut=0:frame-threads=2", fps, fps),
			"-crf", strconv.Itoa(crf + 2),
		}
	case "h264_videotoolbox":
		return []string{
			"-c:v", "h264_videotoolbox",
			"-profile:v", "baseline",
			"-bf", "0",
			"-g", g,
			"-realtime", "1",
			"-b:v", strconv.Itoa(bitrate) + "k",
			"-pix_fmt", "yuv420p",
		}
	case "hevc_videotoolbox":
		return []string{
			"-c:v", "hevc_videotoolbox",
			"-profile:v", "main",
			"-bf", "0",
			"-g", g,
			"-realtime", "1",
			"-b:v", strconv.Itoa(bitrate*7/10) + "k",
			"-pix_fmt", "yuv420p",
			"-tag:v", "hvc1",
		}
	case "h264_nvenc":
		return []string{
			"-c:v", "h264_nvenc",
			"-preset", "p1",
			"-tune", "ll",
			"-rc", "cbr",
			"-b:v", strconv.Itoa(bitrate) + "k",
			"-g", g,
			"-bf", "0",
			"-pix_fmt", "yuv420p",
		}
	case "hevc_nvenc":
		return []string{
			"-c:v", "hevc_nvenc",
			"-preset", "p1",
			"-tune", "ll",
			"-rc", "cbr",
			"-b:v", strconv.Itoa(bitrate*7/10) + "k",
			"-g", g,
			"-bf", "0",
			"-pix_fmt", "yuv420p",
			"-tag:v", "hvc1",
		}
	case "h264_qsv", "h264_amf":
		return []string{
			"-c:v", encName,
			"-bf", "0",
			"-g", g,
			"-b:v", strconv.Itoa(bitrate) + "k",
			"-pix_fmt", "yuv420p",
		}
	case "hevc_qsv", "hevc_amf":
		return []string{
			"-c:v", encName,
			"-bf", "0",
			"-g", g,
			"-b:v", strconv.Itoa(bitrate*7/10) + "k",
			"-pix_fmt", "yuv420p",
			"-tag:v", "hvc1",
		}
	case "h264_vaapi", "hevc_vaapi":
		br := bitrate
		if strings.HasPrefix(encName, "hevc") {
			br = bitrate * 7 / 10
		}
		out := []string{"-c:v", encName, "-bf", "0", "-g", g, "-b:v", strconv.Itoa(br) + "k"}
		if strings.HasPrefix(encName, "hevc") {
			out = append(out, "-tag:v", "hvc1")
		}
		return out
	default:
		return []string{"-c:v", encName, "-pix_fmt", "yuv420p", "-g", g, "-bf", "0"}
	}
}

func deskQualityToCRF(quality int) int {
	crf := 40 - quality/4
	if crf < 16 {
		crf = 16
	}
	if crf > 32 {
		crf = 32
	}
	return crf
}

func deskQualityToBitrateK(quality int) int {
	br := 800 + quality*40
	if br < 1200 {
		br = 1200
	}
	if br > 12000 {
		br = 12000
	}
	return br
}

func deskEvenSize(w, h int, scale float64) (int, int) {
	if scale <= 0 || scale > 1 {
		scale = 0.5
	}
	ow := int(float64(w) * scale)
	oh := int(float64(h) * scale)
	if ow%2 != 0 {
		ow--
	}
	if oh%2 != 0 {
		oh--
	}
	if ow < 16 {
		ow = 16
	}
	if oh < 16 {
		oh = 16
	}
	return ow, oh
}

func clampDeskFPS(fps int) int {
	if fps < 1 {
		return 8
	}
	if fps > 28 {
		return 28
	}
	return fps
}

type deskVideoOpts struct {
	Codec         string
	Quality       int
	FPS           int
	Scale         float64
	AllowSoftHEVC bool
}

func startH264Pipe(mon deskMonitorInfo, scale float64, fps int) (*h264Pipe, error) {
	return startDeskVideoPipe(mon, deskVideoOpts{Codec: "h264", Quality: 80, FPS: fps, Scale: scale})
}

func startDeskVideoPipe(mon deskMonitorInfo, opt deskVideoOpts) (*h264Pipe, error) {
	if !ffmpegAvailable() {
		return nil, fmt.Errorf("ffmpeg not found")
	}
	fps := clampDeskFPS(opt.FPS)
	enc, err := deskPickEncoder(opt.Codec, opt.AllowSoftHEVC || opt.Codec == "h265")
	if err != nil {
		return nil, err
	}
	w, h := deskEvenSize(mon.Width, mon.Height, opt.Scale)
	vf := fmt.Sprintf("scale=%d:%d", w, h)
	args := []string{"-hide_banner", "-loglevel", "error", "-f"}
	switch runtime.GOOS {
	case "windows":
		args = append(args, "gdigrab", "-framerate", strconv.Itoa(fps),
			"-offset_x", strconv.Itoa(mon.X), "-offset_y", strconv.Itoa(mon.Y),
			"-video_size", fmt.Sprintf("%dx%d", mon.Width, mon.Height), "-i", "desktop")
	case "darwin":
		base := deskAVFScreenIndex()
		if base < 0 {
			return nil, fmt.Errorf("avfoundation screen-capture device not found")
		}
		idx := base
		if mon.ID > 1 {
			idx = base + (mon.ID - 1)
		}
		args = append(args, "avfoundation", "-capture_cursor", "1", "-framerate", strconv.Itoa(fps),
			"-i", fmt.Sprintf("%d:none", idx))
	default:
		disp := os.Getenv("DISPLAY")
		if disp == "" {
			disp = ":0"
		}
		grab := fmt.Sprintf("%s+%d,%d", disp, mon.X, mon.Y)
		args = append(args, "x11grab", "-draw_mouse", "1", "-framerate", strconv.Itoa(fps),
			"-video_size", fmt.Sprintf("%dx%d", mon.Width, mon.Height), "-i", grab)
	}
	args = append(args, "-vf", vf)
	args = append(args, deskEncoderArgs(enc.Name, opt.Quality, fps)...)
	args = append(args,
		"-f", "mp4", "-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"pipe:1",
	)
	cmd := exec.Command("ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &h264Pipe{cmd: cmd, stdout: stdout, codec: enc.Codec, enc: enc.Name, qual: opt.Quality, fps: fps}, nil
}

func startH264RawPipe(w, h, fps int) (*h264Pipe, error) {
	return startDeskVideoRawPipe(w, h, deskVideoOpts{Codec: "h264", Quality: 80, FPS: fps, AllowSoftHEVC: true})
}

func startDeskVideoRawPipe(w, h int, opt deskVideoOpts) (*h264Pipe, error) {
	if !ffmpegAvailable() {
		return nil, fmt.Errorf("ffmpeg not found")
	}
	fps := clampDeskFPS(opt.FPS)
	if w%2 != 0 {
		w--
	}
	if h%2 != 0 {
		h--
	}
	if w < 16 {
		w = 16
	}
	if h < 16 {
		h = 16
	}
	enc, err := deskPickEncoder(opt.Codec, opt.AllowSoftHEVC || opt.Codec == "h265")
	if err != nil {
		return nil, err
	}
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", w, h),
		"-r", strconv.Itoa(fps),
		"-i", "pipe:0",
	}
	args = append(args, deskEncoderArgs(enc.Name, opt.Quality, fps)...)
	args = append(args,
		"-f", "mp4", "-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"pipe:1",
	)
	cmd := exec.Command("ffmpeg", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	return &h264Pipe{
		cmd: cmd, stdout: stdout, stdin: stdin,
		rawW: w, rawH: h, codec: enc.Codec, enc: enc.Name, qual: opt.Quality, fps: fps,
	}, nil
}

func (p *h264Pipe) WriteFrame(img image.Image) error {
	if p == nil || p.stdin == nil {
		return fmt.Errorf("video raw stdin closed")
	}
	frame := scaleImageNN(img, p.rawW, p.rawH)
	_, err := p.stdin.Write(frame.Pix)
	return err
}

func (p *h264Pipe) Read(b []byte) (int, error) {
	return p.stdout.Read(b)
}

func (p *h264Pipe) Close() error {
	var err error
	p.once.Do(func() {
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
		if p.stdout != nil {
			_ = p.stdout.Close()
		}
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
			err = p.cmd.Wait()
		}
	})
	return err
}
