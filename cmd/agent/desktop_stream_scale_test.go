package main

import "testing"

func TestDeskQualityEncodeScaleAuto(t *testing.T) {
	q := deskQuality{
		Scale: 0.5, Quality: 82, FPS: 20,
		ClientW: 1280, ClientH: 720, DPR: 1, Sharpness: 1.35, AutoScale: true,
	}
	// Target width ≈ 1280 * 1 * 1.35 = 1728 → scale vs 1920 ≈ 0.9
	got := q.encodeScale(1920, 1080)
	if got < 0.85 || got > 0.95 {
		t.Fatalf("auto encodeScale got %v want ~0.9", got)
	}
}

func TestDeskQualityEncodeScaleCapsRetina(t *testing.T) {
	q := deskQuality{
		ClientW: 960, ClientH: 540, DPR: 2, Sharpness: 1.2, AutoScale: true, Scale: 0.9,
	}
	// Cap physical encode at CSS * 2 * sharpness, not full 2×dpr blow-up beyond maxW.
	got := q.encodeScale(3840, 2160)
	if got > 1 || got < 0.4 {
		t.Fatalf("retina encodeScale out of range: %v", got)
	}
	// Should be well below 1 for 4K host in a small stage.
	if got > 0.7 {
		t.Fatalf("expected downscale for 4K→small stage, got %v", got)
	}
}

func TestDeskQualityEncodeScaleManualFallback(t *testing.T) {
	q := deskQuality{Scale: 0.7, AutoScale: false}
	got := q.encodeScale(1920, 1080)
	if got < 0.69 || got > 0.71 {
		t.Fatalf("manual scale got %v want 0.7", got)
	}
}

func TestDerefInt(t *testing.T) {
	v := 1440
	if derefInt(&v, 1920) != 1440 {
		t.Fatal("deref live")
	}
	if derefInt(nil, 1920) != 1920 {
		t.Fatal("deref nil")
	}
	z := 0
	if derefInt(&z, 1080) != 1080 {
		t.Fatal("deref zero")
	}
}
