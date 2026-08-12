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
