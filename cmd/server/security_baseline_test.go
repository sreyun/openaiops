package main

import "testing"

func TestDiffHostFindings(t *testing.T) {
	prev := []HostFinding{
		{Category: "cve", ID: "1", Title: "old", Level: "medium", CVE: "CVE-1"},
		{Category: "hardening", ID: "ssh", Title: "ssh root", Level: "high"},
	}
	cur := []HostFinding{
		{Category: "cve", ID: "1", Title: "old", Level: "critical", CVE: "CVE-1"}, // worsened
		{Category: "malware", ID: "m1", Title: "new malware", Level: "high"},      // added
	}
	d := diffHostFindings(prev, cur, "prev-1")
	if d == nil {
		t.Fatal("expected diff")
	}
	if d.Added != 1 || d.Removed != 1 || d.Worsened != 1 {
		t.Fatalf("got added=%d removed=%d worsened=%d", d.Added, d.Removed, d.Worsened)
	}
	if d.PreviousScanID != "prev-1" {
		t.Fatalf("prev id=%q", d.PreviousScanID)
	}
}

func TestDiffWebFindings(t *testing.T) {
	prev := []WebFinding{{TemplateID: "t1", MatcherName: "m", URL: "/a", Severity: "high", Name: "A"}}
	cur := []WebFinding{
		{TemplateID: "t1", MatcherName: "m", URL: "/a", Severity: "low", Name: "A"},
		{TemplateID: "t2", MatcherName: "m", URL: "/b", Severity: "critical", Name: "B"},
	}
	d := diffWebFindings(prev, cur, "p")
	if d.Improved != 1 || d.Added != 1 || d.Removed != 0 {
		t.Fatalf("improved=%d added=%d removed=%d", d.Improved, d.Added, d.Removed)
	}
}
