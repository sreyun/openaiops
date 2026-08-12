//go:build linux

package main

import (
	"os"
	"testing"
)

func TestLinuxDistroIDReadsOsRelease(t *testing.T) {
	// Smoke: function must not panic; empty is OK on exotic hosts.
	_ = linuxDistroID()
	_ = linuxIsKylinFamily()
}

func TestLinuxH264PreferWhenFFmpegAndDisplay(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not installed")
	}
	oldDisp := os.Getenv("DISPLAY")
	oldWay := os.Getenv("WAYLAND_DISPLAY")
	oldType := os.Getenv("XDG_SESSION_TYPE")
	defer func() {
		_ = os.Setenv("DISPLAY", oldDisp)
		_ = os.Setenv("WAYLAND_DISPLAY", oldWay)
		_ = os.Setenv("XDG_SESSION_TYPE", oldType)
	}()

	_ = os.Setenv("DISPLAY", ":0")
	_ = os.Unsetenv("WAYLAND_DISPLAY")
	_ = os.Setenv("XDG_SESSION_TYPE", "x11")
	if !deskH264Usable() {
		t.Fatal("expected H.264 usable with DISPLAY+ffmpeg")
	}
	if deskPreferredCodec() != "h264" {
		t.Fatalf("prefer=%q want h264", deskPreferredCodec())
	}
	if deskNeedsRawH264() {
		t.Fatal("X11 should use continuous x11grab, not raw")
	}
}

func TestLinuxNeedsRawOnWaylandWithGrim(t *testing.T) {
	if !ffmpegAvailable() || !lookPathOK("grim") {
		t.Skip("need ffmpeg+grim")
	}
	oldDisp := os.Getenv("DISPLAY")
	oldWay := os.Getenv("WAYLAND_DISPLAY")
	oldType := os.Getenv("XDG_SESSION_TYPE")
	defer func() {
		_ = os.Setenv("DISPLAY", oldDisp)
		_ = os.Setenv("WAYLAND_DISPLAY", oldWay)
		_ = os.Setenv("XDG_SESSION_TYPE", oldType)
	}()

	_ = os.Unsetenv("DISPLAY")
	_ = os.Setenv("WAYLAND_DISPLAY", "wayland-0")
	_ = os.Setenv("XDG_SESSION_TYPE", "wayland")
	if !deskH264Usable() {
		t.Fatal("Wayland+grim+ffmpeg should allow H.264")
	}
	if !deskNeedsRawH264() {
		t.Fatal("Wayland should prefer grim→raw H.264")
	}
	if deskPreferredCodec() != "h264" {
		t.Fatalf("prefer=%q want h264", deskPreferredCodec())
	}
}

func TestLinuxCaptureFailHintKylin(t *testing.T) {
	h := linuxCaptureFailHint(":0", true, os.ErrNotExist)
	if h == "" {
		t.Fatal("empty hint")
	}
}
