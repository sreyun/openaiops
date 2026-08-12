package main

import (
	"testing"

	"aiops-monitor/shared"
)

// TestReportAppliesNestedFolderID exercises handleReport → applyAgentFolderHint
// for a nested asset-tree node (install intent).
func TestReportAppliesNestedFolderID(t *testing.T) {
	srv, token := newTestServer(t)
	srv.cfg.cfg.HostFolders = []HostFolderNode{{
		ID: "prod", Name: "生产", Children: []HostFolderNode{{ID: "db", Name: "DB"}},
	}}
	srv.cfg.cfg.HostFolderAssign = map[string]string{}

	const hostID = "host-folder-nested"
	const fp = "fp-folder-nested-1"
	rr := postJSON(t, srv.handleRegister, "/api/v1/agent/register", map[string]string{
		"host_id": hostID, "hostname": "node-db", "token": token, "fingerprint": fp,
	})
	if rr.Code != 200 {
		t.Fatalf("register: %d %s", rr.Code, rr.Body)
	}

	rr = postJSON(t, srv.handleReport, "/api/v1/agent/report", shared.Report{
		HostID: hostID, Hostname: "node-db", Fingerprint: fp,
		Category: "ignored", FolderID: "db",
		Metrics: shared.Metrics{CPUPercent: 1},
	})
	if rr.Code != 200 {
		t.Fatalf("report: %d %s", rr.Code, rr.Body)
	}
	if got := srv.cfg.hostFolderOf(hostID); got != "db" {
		t.Fatalf("folder assign = %q, want db", got)
	}
	if srv.cfg.cfg.Categories[hostID] != "DB" {
		t.Fatalf("category leaf = %q, want DB", srv.cfg.cfg.Categories[hostID])
	}
}
