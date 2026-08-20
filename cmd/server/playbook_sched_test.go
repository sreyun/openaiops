package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestParseHHMM(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"03:30", 210, true},
		{"00:00", 0, true},
		{"23:59", 1439, true},
		{"9:05", 545, true},
		{"24:00", 0, false},
		{"12:60", 0, false},
		{"abc", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parseHHMM(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseHHMM(%q) = %d,%v; want %d,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func newTestPM(t *testing.T) *playbookManager {
	t.Helper()
	cs, err := NewConfigStore(filepath.Join(t.TempDir(), "cfg.json"), nil)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	return newPlaybookManager(cs)
}

func mustUpsert(t *testing.T, pm *playbookManager, name string, sc *PlaybookSchedule) string {
	t.Helper()
	pb, err := pm.Upsert(Playbook{
		Name:     name,
		Steps:    []PlaybookStep{{Name: "s", Command: "echo hi", Target: "all", TimeoutSec: 30}},
		Schedule: sc,
	})
	if err != nil {
		t.Fatalf("Upsert(%s): %v", name, err)
	}
	return pb.ID
}

func fireIDs(due []Playbook) map[string]bool {
	m := map[string]bool{}
	for _, p := range due {
		m[p.ID] = true
	}
	return m
}

// A daily schedule fires exactly once when a tick crosses the scheduled minute,
// never retroactively on the first tick, and not again after the window passes.
func TestScheduleDaily(t *testing.T) {
	pm := newTestPM(t)
	id := mustUpsert(t, pm, "daily", &PlaybookSchedule{Enabled: true, Kind: "daily", At: "03:00"})
	at := func(h, m, s int) time.Time { return time.Date(2026, 7, 10, h, m, s, 0, time.Local) }

	if due := pm.dueSchedules(at(2, 59, 30)); len(due) != 0 {
		t.Fatalf("first tick before 03:00 must not fire, got %d", len(due))
	}
	if due := pm.dueSchedules(at(3, 0, 15)); !fireIDs(due)[id] {
		t.Fatalf("tick crossing 03:00 must fire")
	}
	pm.clearSchedBusy(id) // simulate the run finishing
	if due := pm.dueSchedules(at(3, 1, 0)); len(due) != 0 {
		t.Fatalf("tick past the window must not re-fire")
	}
}

// An interval schedule fires one interval after its baseline tick, not before.
func TestScheduleInterval(t *testing.T) {
	pm := newTestPM(t)
	id := mustUpsert(t, pm, "interval", &PlaybookSchedule{Enabled: true, Kind: "interval", IntervalMin: 5})
	base := time.Date(2026, 7, 10, 10, 0, 0, 0, time.Local)

	if due := pm.dueSchedules(base); len(due) != 0 {
		t.Fatalf("interval first tick must establish a baseline, not fire")
	}
	if due := pm.dueSchedules(base.Add(4 * time.Minute)); len(due) != 0 {
		t.Fatalf("interval must not fire before the interval elapses")
	}
	if due := pm.dueSchedules(base.Add(5 * time.Minute)); !fireIDs(due)[id] {
		t.Fatalf("interval must fire once the interval elapses")
	}
}

// Weekly fires only on its weekday; a disabled schedule never fires.
func TestScheduleWeeklyAndDisabled(t *testing.T) {
	pm := newTestPM(t)
	day := time.Date(2026, 7, 10, 0, 0, 0, 0, time.Local)
	wd := int(day.Weekday())
	sameID := mustUpsert(t, pm, "same", &PlaybookSchedule{Enabled: true, Kind: "weekly", Weekday: wd, At: "03:00"})
	otherID := mustUpsert(t, pm, "other", &PlaybookSchedule{Enabled: true, Kind: "weekly", Weekday: (wd + 1) % 7, At: "03:00"})
	offID := mustUpsert(t, pm, "off", &PlaybookSchedule{Enabled: false, Kind: "daily", At: "03:00"})

	at := func(h, m, s int) time.Time { return time.Date(2026, 7, 10, h, m, s, 0, time.Local) }
	pm.dueSchedules(at(2, 59, 30)) // baseline
	due := fireIDs(pm.dueSchedules(at(3, 0, 15)))
	if !due[sameID] {
		t.Errorf("weekly on the matching weekday must fire")
	}
	if due[otherID] {
		t.Errorf("weekly on a different weekday must not fire")
	}
	if due[offID] {
		t.Errorf("a disabled schedule must never fire")
	}
}

// A run already in flight (schedBusy) must not be fired again by a later tick.
func TestScheduleSkipsBusy(t *testing.T) {
	pm := newTestPM(t)
	id := mustUpsert(t, pm, "interval", &PlaybookSchedule{Enabled: true, Kind: "interval", IntervalMin: 5})
	base := time.Date(2026, 7, 10, 10, 0, 0, 0, time.Local)
	pm.dueSchedules(base)                                         // baseline
	if !fireIDs(pm.dueSchedules(base.Add(5 * time.Minute)))[id] { // fires, sets schedBusy
		t.Fatalf("expected first fire")
	}
	// Still "running" (busy not cleared): a later due tick must skip it.
	if due := pm.dueSchedules(base.Add(11 * time.Minute)); len(due) != 0 {
		t.Fatalf("busy playbook must not fire again")
	}
	pm.clearSchedBusy(id)
	if !fireIDs(pm.dueSchedules(base.Add(17 * time.Minute)))[id] {
		t.Fatalf("after clearing busy, next due tick must fire")
	}
}
