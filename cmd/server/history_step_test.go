package main

import (
	"testing"

	"aiops-monitor/shared"
)

func TestAdaptiveHistoryStep(t *testing.T) {
	now := int64(1_700_000_000)
	cases := []struct {
		hours int64
		min   int64
		max   int64
	}{
		{1, 5, 15},
		{6, 40, 50},
		{24, 170, 190},
		{168, 1200, 1320},
		{336, 2400, 2700},
	}
	for _, c := range cases {
		step := adaptiveHistoryStep(now-c.hours*3600, now)
		if step < c.min || step > c.max {
			t.Fatalf("%dh step=%d want [%d,%d]", c.hours, step, c.min, c.max)
		}
	}
}

func TestDownsampleSamples(t *testing.T) {
	src := make([]shared.Sample, 1000)
	for i := range src {
		src[i].Timestamp = int64(i)
	}
	out := downsampleSamples(src, 100)
	if len(out) != 100 {
		t.Fatalf("len=%d", len(out))
	}
	if out[0].Timestamp != 0 || out[len(out)-1].Timestamp != 999 {
		t.Fatalf("endpoints lost: first=%d last=%d", out[0].Timestamp, out[len(out)-1].Timestamp)
	}
}
