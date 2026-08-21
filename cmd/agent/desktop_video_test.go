package main

import "testing"

func TestDeskQualityToCRF(t *testing.T) {
	if got := deskQualityToCRF(100); got > 18 {
		t.Fatalf("q100 crf=%d want ≤18", got)
	}
	if got := deskQualityToCRF(80); got < 18 || got > 24 {
		t.Fatalf("q80 crf=%d want ~20", got)
	}
	if got := deskQualityToCRF(40); got < 28 {
		t.Fatalf("q40 crf=%d want ≥28", got)
	}
}

func TestDeskNegotiateVideoCodecLegacyClient(t *testing.T) {
	// Without client_codecs, only H.264 is allowed (legacy MSE clients).
	if got := deskNegotiateVideoCodec("h265", nil); got != "" && got != "h264" {
		// May be h264 if encoders present, never h265 without client list.
		if got == "h265" {
			t.Fatal("must not pick h265 for legacy client")
		}
	}
	if got := deskNegotiateVideoCodec("h265", []string{"h264"}); got == "h265" {
		t.Fatal("client without h265 must not negotiate h265")
	}
}

func TestDeskNegotiatePrefersHardwareHEVCWhenClientOK(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("no ffmpeg")
	}
	deskProbeEncoders()
	hw := false
	for _, e := range deskH265Encoders() {
		if e.Hardware {
			hw = true
			break
		}
	}
	if !hw {
		t.Skip("no hardware HEVC")
	}
	got := deskNegotiateVideoCodec("auto", []string{"h264", "h265"})
	if got != "h265" && got != "h264" {
		t.Fatalf("unexpected %q", got)
	}
	// Explicit want
	if deskNegotiateVideoCodec("h265", []string{"h264", "h265"}) != "h265" {
		t.Fatal("explicit h265 with client support")
	}
}

func TestDeskEncoderArgsLibx264HasZerolatency(t *testing.T) {
	args := deskEncoderArgs("libx264", 82, 20)
	joined := ""
	for _, a := range args {
		joined += a + " "
	}
	if !containsAll(joined, "libx264", "zerolatency", "crf") {
		t.Fatalf("args missing expected flags: %v", args)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !stringContains(s, p) {
			return false
		}
	}
	return true
}

func stringContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

// 远端分辨率自适应：画面缩放压不出不存在的像素——远端 1024×768、本地窗口 2560×1400 时，
// 只有真的改远端分辨率才谈得上"清晰"。这里钉住"挑哪个模式"的规则。
func TestDeskPickModeMatchesClientWindow(t *testing.T) {
	modes := []deskDisplayMode{
		{W: 3840, H: 2160}, {W: 2560, H: 1440}, {W: 1920, H: 1080},
		{W: 1600, H: 900}, {W: 1280, H: 1024}, {W: 1024, H: 768}, {W: 800, H: 600},
	}
	cases := []struct {
		name         string
		cw, ch       int
		wantW, wantH int
	}{
		{"16:9 窗口挑同比例、不超出的最大模式", 1920, 1080, 1920, 1080},
		{"窗口略小于某个模式时不硬塞更大的", 1700, 950, 1600, 900},
		{"4:3 窗口挑 4:3 模式", 1024, 768, 1024, 768},
		{"超大窗口挑最大同比例模式", 3840, 2160, 3840, 2160},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, ok := deskPickMode(modes, c.cw, c.ch)
			if !ok {
				t.Fatal("没挑出模式")
			}
			if m.W != c.wantW || m.H != c.wantH {
				t.Fatalf("挑了 %d×%d，期望 %d×%d", m.W, m.H, c.wantW, c.wantH)
			}
		})
	}
	if _, ok := deskPickMode(nil, 1920, 1080); ok {
		t.Error("没有可用模式时不该返回成功")
	}
	if _, ok := deskPickMode(modes, 100, 80); ok {
		t.Error("窗口小到不合理时不该乱切分辨率")
	}
}

func TestDeskSortModesDedupesAndOrders(t *testing.T) {
	got := deskSortModes([]deskDisplayMode{
		{W: 1024, H: 768, Freq: 60}, {W: 1920, H: 1080, Freq: 60},
		{W: 1024, H: 768, Freq: 75}, {W: 0, H: 0}, {W: 1280, H: 720},
	})
	if len(got) != 3 {
		t.Fatalf("去重后应当剩 3 个，实际 %d：%+v", len(got), got)
	}
	if got[0].W != 1920 || got[len(got)-1].W != 1024 {
		t.Fatalf("应当按面积从大到小：%+v", got)
	}
}

// 不支持改分辨率的平台必须明确报错，而不是假装成功（UI 据此隐藏入口）。
func TestDeskResolutionUnsupportedIsExplicit(t *testing.T) {
	if _, err := deskApplyResolution(nil, 1920, 1080, 0, 0); err == nil {
		t.Fatal("不支持的平台应当返回错误")
	}
	if err := deskRestoreResolution(nil); err != nil {
		t.Fatalf("不支持的平台恢复应当是空操作：%v", err)
	}
	if modes := deskModesOf(nil); modes != nil {
		t.Fatalf("不支持的平台不该报模式列表：%+v", modes)
	}
}
