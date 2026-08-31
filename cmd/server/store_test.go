package main

import (
	"testing"
	"time"

	"aiops-monitor/shared"
)

// newTestReport builds a minimal valid agent report for store tests.
func newTestReport(hostID, hostname, fp string, cpu float64) shared.Report {
	return shared.Report{
		HostID:      hostID,
		Hostname:    hostname,
		OS:          "linux",
		Fingerprint: fp,
		Metrics: shared.Metrics{
			CPUPercent: cpu,
			CPUCores:   4,
			MemTotal:   8 << 30,
			MemUsed:    4 << 30,
			MemPercent: 50,
		},
	}
}

func TestNewStore(t *testing.T) {
	s := NewStore()
	if s == nil {
		t.Fatal("NewStore returned nil")
	}
	if hosts := s.ListHosts(); len(hosts) != 0 {
		t.Errorf("new store should have no hosts, got %d", len(hosts))
	}
}

func TestBindPGNil(t *testing.T) {
	s := NewStore()
	if err := s.BindPG(nil); err != nil {
		t.Fatalf("BindPG(nil): %v", err)
	}
}

func TestRegisterHost(t *testing.T) {
	t.Run("new host", func(t *testing.T) {
		s := NewStore()
		h := s.RegisterHost("h1", "node-1", "fp-aaa")
		if h == nil {
			t.Fatal("RegisterHost returned nil")
		}
		if h.ID != "h1" || h.Hostname != "node-1" || h.Fingerprint != "fp-aaa" {
			t.Errorf("unexpected host: %+v", h)
		}
		if got := s.ListHosts(); len(got) != 1 {
			t.Errorf("expected 1 host, got %d", len(got))
		}
	})
	t.Run("existing host refuses fingerprint overwrite", func(t *testing.T) {
		s := NewStore()
		s.RegisterHost("h1", "node-1", "fp-aaa")
		h := s.RegisterHost("h1", "node-1-renamed", "fp-bbb")
		if h.Fingerprint != "fp-aaa" {
			t.Errorf("fingerprint must not be overwritten: got %s", h.Fingerprint)
		}
		if h.Hostname != "node-1-renamed" {
			t.Errorf("hostname not updated: got %s", h.Hostname)
		}
		if got := s.ListHosts(); len(got) != 1 {
			t.Errorf("expected still 1 host, got %d", len(got))
		}
	})
	t.Run("explicit rebind updates fingerprint", func(t *testing.T) {
		s := NewStore()
		s.RegisterHost("h1", "node-1", "fp-aaa")
		h := s.RegisterHostRebindFP("h1", "node-1", "fp-new")
		if h.Fingerprint != "fp-new" {
			t.Errorf("rebind fingerprint: got %s", h.Fingerprint)
		}
	})
	t.Run("recently deleted host is suppressed", func(t *testing.T) {
		s := NewStore()
		s.RegisterHost("h1", "node-1", "fp-aaa")
		if !s.DeleteHost("h1") {
			t.Fatal("DeleteHost returned false")
		}
		// Re-register immediately — should be suppressed.
		h := s.RegisterHost("h1", "node-1", "fp-aaa")
		if h == nil {
			t.Fatal("RegisterHost returned nil for suppressed host")
		}
		// Suppressed registration returns a stub with only the ID set.
		if h.Fingerprint != "" {
			t.Errorf("suppressed host should have empty fingerprint, got %s", h.Fingerprint)
		}
		if got := s.ListHosts(); len(got) != 0 {
			t.Errorf("suppressed host should not appear in list, got %d", len(got))
		}
	})
}

func TestUpsertAuthenticated(t *testing.T) {
	t.Run("valid fingerprint", func(t *testing.T) {
		s := NewStore()
		s.RegisterHost("h1", "node-1", "fp-aaa")
		rep := newTestReport("h1", "node-1", "fp-aaa", 42)
		h, ok := s.UpsertAuthenticated(rep, "fp-aaa")
		if !ok {
			t.Fatal("UpsertAuthenticated rejected a valid fingerprint")
		}
		if h.ID != "h1" {
			t.Errorf("unexpected host id: %s", h.ID)
		}
		if h.Latest == nil || h.Latest.CPUPercent != 42 {
			t.Errorf("latest sample not stored: %+v", h.Latest)
		}
	})
	t.Run("invalid fingerprint", func(t *testing.T) {
		s := NewStore()
		s.RegisterHost("h1", "node-1", "fp-aaa")
		rep := newTestReport("h1", "node-1", "fp-wrong", 42)
		_, ok := s.UpsertAuthenticated(rep, "fp-wrong")
		if ok {
			t.Error("UpsertAuthenticated accepted a mismatched fingerprint")
		}
	})
	t.Run("unregistered host", func(t *testing.T) {
		s := NewStore()
		rep := newTestReport("ghost", "ghost", "fp-aaa", 42)
		_, ok := s.UpsertAuthenticated(rep, "fp-aaa")
		if ok {
			t.Error("UpsertAuthenticated accepted an unregistered host")
		}
	})
	t.Run("empty fingerprint", func(t *testing.T) {
		s := NewStore()
		// Register with empty fingerprint — host exists but fingerprint not bound.
		s.RegisterHost("h1", "node-1", "")
		rep := newTestReport("h1", "node-1", "", 42)
		_, ok := s.UpsertAuthenticated(rep, "")
		if ok {
			t.Error("UpsertAuthenticated accepted an empty fingerprint")
		}
	})
}

func TestListHosts(t *testing.T) {
	s := NewStore()
	s.RegisterHost("h1", "node-1", "fp-aaa")
	s.RegisterHost("h2", "node-2", "fp-bbb")
	hosts := s.ListHosts()
	if len(hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %d", len(hosts))
	}
	// ListHosts returns shallow copies; mutating one must not affect the store.
	hosts[0].Hostname = "tampered"
	again := s.ListHosts()
	gotNode1 := ""
	for _, h := range again {
		if h.ID == "h1" {
			gotNode1 = h.Hostname
		}
	}
	if gotNode1 == "" {
		t.Fatalf("h1 disappeared from list after mutating a copy")
	}
	if gotNode1 == "tampered" {
		t.Errorf("ListHosts did not return a copy; store was mutated (got %q)", gotNode1)
	}
}

func TestGetSamples(t *testing.T) {
	s := NewStore()
	s.RegisterHost("h1", "node-1", "fp-aaa")
	rep := newTestReport("h1", "node-1", "fp-aaa", 10)
	if _, ok := s.UpsertAuthenticated(rep, "fp-aaa"); !ok {
		t.Fatal("upsert failed")
	}
	samples, ok := s.GetSamples("h1")
	if !ok {
		t.Fatal("GetSamples returned ok=false for existing host")
	}
	if len(samples) != 1 {
		t.Errorf("expected 1 sample, got %d", len(samples))
	}
	if _, ok := s.GetSamples("nonexistent"); ok {
		t.Error("GetSamples returned ok=true for missing host")
	}
}

func TestGetHistory(t *testing.T) {
	s := NewStore()
	s.RegisterHost("h1", "node-1", "fp-aaa")
	// Push a few raw samples so the raw tier has data.
	for i := 0; i < 3; i++ {
		rep := newTestReport("h1", "node-1", "fp-aaa", float64(10*i))
		rep.Metrics.CPUPercent = float64(10 * i)
		if _, ok := s.UpsertAuthenticated(rep, "fp-aaa"); !ok {
			t.Fatalf("upsert %d failed", i)
		}
		time.Sleep(10 * time.Millisecond)
	}
	now := time.Now().Unix()

	t.Run("short span uses raw tier", func(t *testing.T) {
		samples, ok := s.GetHistory("h1", now-3600, now)
		if !ok {
			t.Fatal("GetHistory returned ok=false")
		}
		if len(samples) == 0 {
			t.Error("expected raw samples for short span, got none")
		}
	})
	t.Run("medium span uses 1-min tier", func(t *testing.T) {
		// 3-hour span -> 1-min tier. May be empty if no aggregation ran, but
		// must still return ok=true and not error.
		samples, ok := s.GetHistory("h1", now-3*3600, now)
		if !ok {
			t.Fatal("GetHistory returned ok=false for medium span")
		}
		// raw samples fall outside the [<2h] window so the 1-min tier is used;
		// it may legitimately be empty if no 1-min aggregation has occurred.
		_ = samples
	})
	t.Run("long span uses 5-min tier", func(t *testing.T) {
		samples, ok := s.GetHistory("h1", now-72*3600, now)
		if !ok {
			t.Fatal("GetHistory returned ok=false for long span")
		}
		_ = samples
	})
	t.Run("missing host", func(t *testing.T) {
		_, ok := s.GetHistory("ghost", 0, now)
		if ok {
			t.Error("GetHistory returned ok=true for missing host")
		}
	})
}

func TestDeleteHost(t *testing.T) {
	t.Run("existing host", func(t *testing.T) {
		s := NewStore()
		s.RegisterHost("h1", "node-1", "fp-aaa")
		if !s.DeleteHost("h1") {
			t.Fatal("DeleteHost returned false for existing host")
		}
		if got := s.ListHosts(); len(got) != 0 {
			t.Errorf("host still present after delete: %d", len(got))
		}
	})
	t.Run("non-existing host", func(t *testing.T) {
		s := NewStore()
		if s.DeleteHost("ghost") {
			t.Error("DeleteHost returned true for non-existing host")
		}
	})
}

func TestRecentEvents(t *testing.T) {
	s := NewStore()
	s.RegisterHost("h1", "node-1", "fp-aaa")
	rep := newTestReport("h1", "node-1", "fp-aaa", 10)
	rep.Events = []shared.Event{
		{Level: "warning", Source: "probe-a", Message: "service slow"},
		{Level: "critical", Source: "probe-b", Message: "service down"},
	}
	if _, ok := s.UpsertAuthenticated(rep, "fp-aaa"); !ok {
		t.Fatal("upsert failed")
	}
	events := s.RecentEvents()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	// RecentEvents returns newest first.
	if events[0].Message != "service down" {
		t.Errorf("expected newest event first, got %q", events[0].Message)
	}
}

func TestAddLogAndRecentActivity(t *testing.T) {
	s := NewStore()
	s.AddLog(LogEntry{Kind: "操作", Level: "info", Actor: "tester", Message: "first"})
	s.AddLog(LogEntry{Kind: "系统", Level: "warning", Actor: "system", Message: "second"})
	items := s.RecentActivity()
	if len(items) != 2 {
		t.Fatalf("expected 2 activity entries, got %d", len(items))
	}
	// RecentActivity returns newest first.
	if items[0].Message != "second" {
		t.Errorf("expected newest entry first, got %q", items[0].Message)
	}
	if items[1].Message != "first" {
		t.Errorf("expected oldest entry second, got %q", items[1].Message)
	}
}
