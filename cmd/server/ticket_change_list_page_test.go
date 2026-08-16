package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// seedTickets fills a manager with n tickets, each carrying a comment thread and
// an attachment, so the list projection has something to strip.
func seedTickets(t *testing.T, n int) *ticketManager {
	t.Helper()
	tm := newTicketManager()
	blob := strings.Repeat("A", 4096) // stands in for a base64 image
	for i := 0; i < n; i++ {
		tk, err := tm.Create(Ticket{
			Title:       "ticket",
			Description: "why",
			Attachments: []Attachment{{Name: "shot.png", Kind: "image", Data: blob}},
		}, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tm.Comment(tk.ID, "bob", "looking into it",
			[]Attachment{{Name: "log.txt", Kind: "file", Text: blob}}); err != nil {
			t.Fatal(err)
		}
	}
	return tm
}

func TestListTicketsDropsThreadKeepsCounts(t *testing.T) {
	s := &Server{tickets: seedTickets(t, 3)}
	rec := httptest.NewRecorder()
	s.handleListTickets(rec, httptest.NewRequest("GET", "/api/v1/tickets", nil))

	if body := rec.Body.String(); strings.Contains(body, "looking into it") {
		t.Fatalf("list leaked the comment thread: %s", body[:min(len(body), 400)])
	}
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unpaged list must stay a bare array: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	if _, ok := rows[0]["comments"]; ok {
		t.Fatalf("comments must be omitted from list rows: %#v", rows[0])
	}
	if _, ok := rows[0]["attachments"]; ok {
		t.Fatalf("attachments must be omitted from list rows: %#v", rows[0])
	}
	// The counts are what lets the row still say "2 comments / 3 files".
	// Create() mirrors a ticket's own attachments into an opening comment, so
	// one seeded ticket carries 2 comments and 3 attachment payloads.
	if got := rows[0]["comment_count"]; got != float64(2) {
		t.Fatalf("comment_count=%v want 2", got)
	}
	if got := rows[0]["attachment_count"]; got != float64(3) {
		t.Fatalf("attachment_count=%v want 3 (ticket + 2 comments)", got)
	}
	// Fields the list actually renders must survive.
	if rows[0]["title"] != "ticket" || rows[0]["description"] != "why" {
		t.Fatalf("list row lost head fields: %#v", rows[0])
	}
}

func TestGetTicketStillReturnsThread(t *testing.T) {
	tm := seedTickets(t, 1)
	s := &Server{tickets: tm, incidents: newIncidentManager()}
	id := tm.List("")[0].ID

	req := httptest.NewRequest("GET", "/api/v1/tickets/1", nil)
	req.SetPathValue("id", itoa64(id))
	rec := httptest.NewRecorder()
	s.handleGetTicket(rec, req)

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	comments, _ := got["comments"].([]any)
	if len(comments) != 2 {
		t.Fatalf("detail must still carry the thread, got %d comments", len(comments))
	}
	if !strings.Contains(rec.Body.String(), "looking into it") {
		t.Fatal("detail lost the comment text")
	}
}

func TestListTicketsPaging(t *testing.T) {
	s := &Server{tickets: seedTickets(t, 7)}

	get := func(qs string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handleListTickets(rec, httptest.NewRequest("GET", "/api/v1/tickets?"+qs, nil))
		var env map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("paged list must be an envelope: %v", err)
		}
		return env
	}

	first := get("limit=3")
	if got := first["total"]; got != float64(7) {
		t.Fatalf("total=%v want 7", got)
	}
	if items, _ := first["items"].([]any); len(items) != 3 {
		t.Fatalf("want 3 items, got %d", len(items))
	}

	last := get("limit=3&offset=6")
	if items, _ := last["items"].([]any); len(items) != 1 {
		t.Fatalf("tail page: want 1 item, got %d", len(items))
	}

	// An offset past the end must clamp, not panic or 500.
	past := get("limit=3&offset=99")
	if items, _ := past["items"].([]any); len(items) != 0 {
		t.Fatalf("out-of-range offset: want 0 items, got %d", len(items))
	}
	if got := past["offset"]; got != float64(7) {
		t.Fatalf("offset should clamp to total, got %v", got)
	}
}

func TestListChangesDropsPlansKeepsFlags(t *testing.T) {
	cm := newChangeManager()
	if _, err := cm.Upsert(ChangeRecord{
		Title:        "deploy web",
		Plan:         strings.Repeat("step\n", 500),
		RollbackPlan: "helm rollback",
	}, "alice"); err != nil {
		t.Fatal(err)
	}
	s := &Server{changes: cm}

	rec := httptest.NewRecorder()
	s.handleListChanges(rec, httptest.NewRequest("GET", "/api/v1/changes", nil))
	if strings.Contains(rec.Body.String(), "helm rollback") {
		t.Fatalf("list leaked the rollback plan: %s", rec.Body.String())
	}

	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unpaged list must stay a bare array: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if _, ok := rows[0]["plan"]; ok {
		t.Fatalf("plan must be omitted from list rows: %#v", rows[0])
	}
	if rows[0]["has_plan"] != true || rows[0]["has_rollback_plan"] != true {
		t.Fatalf("has_* flags lost: %#v", rows[0])
	}
	if rows[0]["has_test_plan"] != false {
		t.Fatalf("has_test_plan should be false: %#v", rows[0])
	}
	if rows[0]["title"] != "deploy web" {
		t.Fatalf("list row lost head fields: %#v", rows[0])
	}
}

func TestListChangesPagingKeepsManagerIntact(t *testing.T) {
	cm := newChangeManager()
	for i := 0; i < 5; i++ {
		if _, err := cm.Upsert(ChangeRecord{Title: "c", Plan: "do the thing"}, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{changes: cm}

	rec := httptest.NewRecorder()
	s.handleListChanges(rec, httptest.NewRequest("GET", "/api/v1/changes?limit=2&offset=2", nil))
	var env struct {
		Items  []map[string]any `json:"items"`
		Total  int              `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Total != 5 || len(env.Items) != 2 || env.Limit != 2 || env.Offset != 2 {
		t.Fatalf("bad envelope: %+v", env)
	}
	// Projecting must not blank the stored records — the detail endpoint and
	// every internal reader (CMDB, effect analysis) still need the plans.
	for _, c := range cm.List() {
		if c.Plan != "do the thing" {
			t.Fatalf("projection mutated the manager: %#v", c)
		}
	}
}
