package main

import (
	"testing"
	"time"
)

func TestOpsLinkMergeAndNormalize(t *testing.T) {
	links := mergeOpsLinks(nil,
		OpsLink{Type: "host", ID: "h1", Role: "affects"},
		OpsLink{Type: "HOST", ID: "h1", Role: "affects"}, // dedupe
		OpsLink{Type: "bad", ID: "x"},                    // invalid
		OpsLink{Type: "slo", ID: "s1"},
	)
	if len(links) != 2 {
		t.Fatalf("want 2 links, got %d %#v", len(links), links)
	}
	links = removeOpsLink(links, "host", "h1", "")
	if len(links) != 1 || links[0].Type != "slo" {
		t.Fatalf("remove failed: %#v", links)
	}
}

func TestTicketKindInferAndEscalateLinks(t *testing.T) {
	tm := newTicketManager()
	tk, err := tm.Create(Ticket{
		Title: "from inc", IncidentID: 9,
		Links: []OpsLink{hostOpsLink("h1", "web-01"), sloOpsLink("slo-a")},
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Kind != "incident" {
		t.Fatalf("kind=%s want incident", tk.Kind)
	}
	if tk.SLOID != "slo-a" {
		t.Fatalf("slo_id=%s", tk.SLOID)
	}
	foundHost := false
	for _, l := range tk.Links {
		if l.Type == "host" && l.ID == "h1" {
			foundHost = true
		}
	}
	if !foundHost {
		t.Fatalf("missing host link: %#v", tk.Links)
	}

	sr, err := tm.Create(Ticket{Title: "开通账号", CatalogItem: "account_provision", Kind: "service_request"}, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if sr.Kind != "service_request" {
		t.Fatalf("kind=%s", sr.Kind)
	}
	list := tm.List("service_request")
	if len(list) != 1 || list[0].ID != sr.ID {
		t.Fatalf("kind filter failed: %#v", list)
	}
}

func TestChangeStatusMachineAndFreezeGate(t *testing.T) {
	cm := newChangeManager()
	rec, err := cm.Upsert(ChangeRecord{
		Title: "risky", Kind: "emergency", Risk: "high", Status: ChangeDraft,
		HostIDs: []string{"h1"},
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cm.Transition(rec.ID, "start", "alice", true); err == nil {
		t.Fatal("expected freeze+high-risk start to fail without approval")
	}
	if _, err := cm.Transition(rec.ID, "submit", "alice", true); err != nil {
		t.Fatal(err)
	}
	if _, err := cm.Transition(rec.ID, "approve", "bob", true); err != nil {
		t.Fatal(err)
	}
	out, err := cm.Transition(rec.ID, "start", "alice", true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != ChangeInProgress {
		t.Fatalf("status=%s", out.Status)
	}
	out, err = cm.Transition(rec.ID, "complete", "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != ChangeCompleted {
		t.Fatalf("status=%s", out.Status)
	}
}

func TestChangeLegacyStatusAndSQLBridge(t *testing.T) {
	cm := newChangeManager()
	cm.Import([]ChangeRecord{{ID: 1, Title: "old", Status: "planned", Kind: "other", Risk: "low", StartedAt: 1, CreatedAt: 1}})
	got, ok := cm.Get(1)
	if !ok || got.Status != ChangeDraft {
		t.Fatalf("legacy planned map failed: %#v", got)
	}
	rec, err := cm.UpsertFromSQLChange("sql-1", "SQL DDL · db", "create index", "sql", "high", "alice", ChangePendingApproval)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.SQLChangeIDs) != 1 || rec.SQLChangeIDs[0] != "sql-1" {
		t.Fatalf("sql link missing: %#v", rec)
	}
	again, err := cm.UpsertFromSQLChange("sql-1", "SQL DDL · db", "updated", "sql", "high", "alice", ChangeApproved)
	if err != nil || again.ID != rec.ID {
		t.Fatalf("upsert should reuse: %#v err=%v", again, err)
	}
	if again.Summary != "updated" {
		t.Fatalf("summary=%s", again.Summary)
	}
}

func TestIncidentRaiseWritesLinks(t *testing.T) {
	im := newIncidentManager()
	id, created := im.raise("alert|h1|cpu", "CPU high", "critical", "alert", "h1", "web-01", "cpu")
	if !created || id == 0 {
		t.Fatal("raise failed")
	}
	inc, ok := im.Get(id)
	if !ok {
		t.Fatal("missing incident")
	}
	hasHost, hasAlert := false, false
	for _, l := range inc.Links {
		if l.Type == "host" && l.ID == "h1" {
			hasHost = true
		}
		if l.Type == "alert" {
			hasAlert = true
		}
	}
	if !hasHost || !hasAlert {
		t.Fatalf("links incomplete: %#v", inc.Links)
	}
	id2, _ := im.raise("slo/slo-x", "budget", "critical", "slo", "", "", "slo")
	inc2, _ := im.Get(id2)
	foundSLO := false
	for _, l := range inc2.Links {
		if l.Type == "slo" && l.ID == "slo-x" {
			foundSLO = true
		}
	}
	if !foundSLO {
		t.Fatalf("slo link missing: %#v", inc2.Links)
	}
}

func TestServiceRequestCatalogDefaults(t *testing.T) {
	cs := &ConfigStore{cfg: ServerConfig{}}
	cat := cs.ServiceRequestCatalog()
	if len(cat) < 6 {
		t.Fatalf("want default catalog, got %d", len(cat))
	}
	if _, ok := cs.FindServiceRequestCatalogItem("account_provision"); !ok {
		t.Fatal("account_provision missing")
	}
}

func TestChangeUpsertPreservesSQLLinks(t *testing.T) {
	cm := newChangeManager()
	rec, err := cm.UpsertFromSQLChange("sql-keep", "SQL DDL", "idx", "sql", "high", "alice", ChangePendingApproval)
	if err != nil {
		t.Fatal(err)
	}
	// Partial UI-style update without association fields must not wipe SQL bridge.
	out, err := cm.Upsert(ChangeRecord{
		ID: rec.ID, Title: "SQL DDL updated", Kind: "sql", Risk: "high", Status: ChangePendingApproval,
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.SQLChangeIDs) != 1 || out.SQLChangeIDs[0] != "sql-keep" {
		t.Fatalf("sql_change_ids wiped: %#v", out.SQLChangeIDs)
	}
	found := false
	for _, l := range out.Links {
		if l.Type == "sql_change" && l.ID == "sql-keep" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sql_change link wiped: %#v", out.Links)
	}
}

func TestChangeIDsStartAtOne(t *testing.T) {
	cm := newChangeManager()
	rec, err := cm.Upsert(ChangeRecord{Title: "first", Kind: "other"}, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != 1 {
		t.Fatalf("first change id=%d want 1", rec.ID)
	}
	tm := newTicketManager()
	tk, err := tm.Create(Ticket{Title: "t1"}, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if tk.ID != 1 {
		t.Fatalf("first ticket id=%d want 1", tk.ID)
	}
}

func TestSyncSQLChangeRecordStatusFlow(t *testing.T) {
	s := &Server{
		changes:    newChangeManager(),
		sqlChanges: newSQLChangeRequestManager(),
		store:      NewStore(),
	}
	cr := s.sqlChanges.Create(MySQLConnection{ID: "c1", Name: "db1", Env: "prod"}, "CREATE INDEX i ON t(a)", "reason", "alice", "ddl", time.Now())
	rec, err := s.bridgeSQLChangeToRecord(cr, ChangePendingApproval)
	if err != nil {
		t.Fatal(err)
	}
	s.sqlChanges.SetChangeID(cr.ID, rec.ID)
	cr, _ = s.sqlChanges.Approve(cr.ID, "bob", time.Now())
	s.syncSQLChangeRecordStatus(cr, "bob")
	got, ok := s.changes.Get(rec.ID)
	if !ok || got.Status != ChangeApproved {
		t.Fatalf("after approve status=%s ok=%v", got.Status, ok)
	}
	cr.Status = "executed"
	cr.Executor = "carol"
	s.syncSQLChangeRecordStatus(cr, "carol")
	got, _ = s.changes.Get(rec.ID)
	if got.Status != ChangeCompleted {
		t.Fatalf("after execute status=%s want completed", got.Status)
	}
}
