package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// Ticket (work order) — a lightweight issue tracker for follow-up work.
//
// A ticket is an assignable, prioritized work item with a status flow and a
// comment thread. It can be spun off from an incident (for the fix / postmortem),
// created as a service request from the catalog, or created standalone.
// Persisted via the DB snapshot so it survives restarts.
// ============================================================================

// TicketComment is one note on a ticket's thread.
type TicketComment struct {
	Ts          int64        `json:"ts"`
	Author      string       `json:"author"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Ticket is a tracked unit of work.
type Ticket struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind,omitempty"`         // incident|service_request|task
	Category    string `json:"category,omitempty"`     // service request category
	CatalogItem string `json:"catalog_item,omitempty"` // service request catalog id
	Priority    string `json:"priority"`               // p1|p2|p3|p4
	Status      string `json:"status"`                 // open|in_progress|resolved|closed
	Assignee    string `json:"assignee,omitempty"`
	Reporter    string `json:"reporter,omitempty"`
	IncidentID  int64  `json:"incident_id,omitempty"`
	SLOID       string `json:"slo_id,omitempty"`
	ChangeID    int64  `json:"change_id,omitempty"`
	SQLChangeID string `json:"sql_change_id,omitempty"`
	Source      string `json:"source,omitempty"` // manual|incident|alert|dashboard|sql|api
	// AIRunID ties the ticket back to the AI answer it was created from
	// (/api/v1/ai/followup). Resolving such a ticket is an objective verdict on
	// that answer, so it feeds the learning loop — see learnFromAIFollowupTicket.
	// Server-set only: Create clears whatever a client sends, or anyone could
	// claim AI provenance and get arbitrary conclusions marked "verified".
	AIRunID     string          `json:"ai_run_id,omitempty"`
	DueAt       int64           `json:"due_at,omitempty"`
	Links       []OpsLink       `json:"links,omitempty"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	Comments    []TicketComment `json:"comments,omitempty"`
	CreatedAt   int64           `json:"created_at"`
	UpdatedAt   int64           `json:"updated_at"`
}

var ticketPriorities = map[string]bool{"p1": true, "p2": true, "p3": true, "p4": true}
var ticketStatuses = map[string]bool{"open": true, "in_progress": true, "resolved": true, "closed": true}
var ticketKinds = map[string]bool{"incident": true, "service_request": true, "task": true}

func normalizeTicketKind(t *Ticket) {
	k := strings.ToLower(strings.TrimSpace(t.Kind))
	if ticketKinds[k] {
		t.Kind = k
		return
	}
	if t.IncidentID > 0 {
		t.Kind = "incident"
		return
	}
	if strings.TrimSpace(t.CatalogItem) != "" || strings.EqualFold(t.Source, "service_request") {
		t.Kind = "service_request"
		return
	}
	t.Kind = "task"
}

func syncTicketLinkIndexes(t *Ticket) {
	if t.IncidentID > 0 {
		t.Links = mergeOpsLinks(t.Links, incidentOpsLink(t.IncidentID, "caused_by"))
	}
	if t.SLOID != "" {
		t.Links = mergeOpsLinks(t.Links, sloOpsLink(t.SLOID))
	}
	if t.ChangeID > 0 {
		t.Links = mergeOpsLinks(t.Links, changeOpsLink(t.ChangeID))
	}
	if t.SQLChangeID != "" {
		t.Links = mergeOpsLinks(t.Links, sqlChangeOpsLink(t.SQLChangeID))
	}
	for _, l := range t.Links {
		switch l.Type {
		case "incident":
			if id := parseOpsLinkInt(l.ID); id > 0 && t.IncidentID == 0 {
				t.IncidentID = id
			}
		case "slo":
			if t.SLOID == "" {
				t.SLOID = l.ID
			}
		case "change":
			if id := parseOpsLinkInt(l.ID); id > 0 && t.ChangeID == 0 {
				t.ChangeID = id
			}
		case "sql_change":
			if t.SQLChangeID == "" {
				t.SQLChangeID = l.ID
			}
		}
	}
}

// ticketManager stores tickets in memory (persisted via the DB snapshot).
type ticketManager struct {
	mu      sync.Mutex
	tickets []Ticket
	nextID  int64
}

func newTicketManager() *ticketManager {
	return &ticketManager{nextID: 0}
}

func (m *ticketManager) find(id int64) *Ticket {
	for i := range m.tickets {
		if m.tickets[i].ID == id {
			return &m.tickets[i]
		}
	}
	return nil
}

// Create adds a new ticket. Priority/status default sensibly when blank/invalid.
func (m *ticketManager) Create(t Ticket, reporter string) (Ticket, error) {
	t.Title = strings.TrimSpace(t.Title)
	if t.Title == "" {
		return Ticket{}, fmt.Errorf("%s", Tz("ticket.title_required"))
	}
	if !ticketPriorities[t.Priority] {
		t.Priority = "p3"
	}
	normalizeTicketKind(&t)
	if t.Source == "" {
		if t.Kind == "incident" && t.IncidentID > 0 {
			t.Source = "incident"
		} else {
			t.Source = "manual"
		}
	}
	t.Links = mergeOpsLinks(nil, t.Links...)
	syncTicketLinkIndexes(&t)
	t.AIRunID = "" // provenance is server-set; see AttachAIRun
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	t.ID = m.nextID
	t.Status = "open"
	t.Reporter = reporter
	t.Attachments = sanitizeAttachments(t.Attachments)
	t.Comments = nil
	now := time.Now().Unix()
	t.CreatedAt, t.UpdatedAt = now, now
	if len(t.Attachments) > 0 {
		t.Comments = append(t.Comments, TicketComment{
			Ts: now, Author: reporter, Text: Tz("ticket.evt_attach", len(t.Attachments)),
			Attachments: t.Attachments,
		})
	}
	m.tickets = append(m.tickets, t)
	return t, nil
}

// ApplySLAAndAssign is called by API layer after Create to set deadlines / OnCall assignee.
func (m *ticketManager) ApplyPostCreate(id int64, apply func(*Ticket)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.find(id); t != nil && apply != nil {
		apply(t)
		t.UpdatedAt = time.Now().Unix()
	}
}

// Update mutates editable fields (title/description/priority/status/assignee/...).
func (m *ticketManager) Update(id int64, in Ticket, actor string) (Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.find(id)
	if t == nil {
		return Ticket{}, fmt.Errorf("%s", Tz("ticket.not_found"))
	}
	if s := strings.TrimSpace(in.Title); s != "" {
		t.Title = s
	}
	t.Description = in.Description
	if ticketPriorities[in.Priority] {
		t.Priority = in.Priority
	}
	if in.Status != "" && ticketStatuses[in.Status] && in.Status != t.Status {
		t.Status = in.Status
		t.Comments = append(t.Comments, TicketComment{Ts: time.Now().Unix(), Author: actor,
			Text: Tz("ticket.evt_status", in.Status)})
	}
	if in.Assignee != t.Assignee {
		t.Assignee = in.Assignee
		who := in.Assignee
		if who == "" {
			who = Tz("ticket.unassigned")
		}
		t.Comments = append(t.Comments, TicketComment{Ts: time.Now().Unix(), Author: actor,
			Text: Tz("ticket.evt_assign", who)})
	}
	if k := strings.ToLower(strings.TrimSpace(in.Kind)); ticketKinds[k] {
		t.Kind = k
	}
	if in.Category != "" {
		t.Category = strings.TrimSpace(in.Category)
	}
	if in.CatalogItem != "" {
		t.CatalogItem = strings.TrimSpace(in.CatalogItem)
	}
	if in.DueAt > 0 {
		t.DueAt = in.DueAt
	}
	if in.SLOID != "" {
		t.SLOID = strings.TrimSpace(in.SLOID)
	}
	if in.ChangeID > 0 {
		t.ChangeID = in.ChangeID
	}
	if in.SQLChangeID != "" {
		t.SQLChangeID = strings.TrimSpace(in.SQLChangeID)
	}
	if len(in.Links) > 0 {
		t.Links = mergeOpsLinks(t.Links, in.Links...)
	}
	normalizeTicketKind(t)
	syncTicketLinkIndexes(t)
	t.UpdatedAt = time.Now().Unix()
	return *t, nil
}

// Comment appends a note to a ticket（允许纯附件、无正文）。
func (m *ticketManager) Comment(id int64, author, text string, atts []Attachment) (Ticket, error) {
	text = strings.TrimSpace(text)
	atts = sanitizeAttachments(atts)
	if text == "" && len(atts) == 0 {
		return Ticket{}, fmt.Errorf("%s", Tz("ticket.comment_required"))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.find(id)
	if t == nil {
		return Ticket{}, fmt.Errorf("%s", Tz("ticket.not_found"))
	}
	if text == "" && len(atts) > 0 {
		text = Tz("ticket.evt_attach", len(atts))
	}
	t.Comments = append(t.Comments, TicketComment{Ts: time.Now().Unix(), Author: author, Text: text, Attachments: atts})
	t.UpdatedAt = time.Now().Unix()
	return *t, nil
}

// Link adds or removes OpsLinks on a ticket.
func (m *ticketManager) Link(id int64, add []OpsLink, removeType, removeID, removeRole, actor string) (Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.find(id)
	if t == nil {
		return Ticket{}, fmt.Errorf("%s", Tz("ticket.not_found"))
	}
	if removeType != "" && removeID != "" {
		t.Links = removeOpsLink(t.Links, removeType, removeID, removeRole)
	}
	if len(add) > 0 {
		before := len(t.Links)
		t.Links = mergeOpsLinks(t.Links, add...)
		if len(t.Links) > before {
			t.Comments = append(t.Comments, TicketComment{
				Ts: time.Now().Unix(), Author: actor,
				Text: "关联更新：" + formatOpsLinksHint(add),
			})
		}
	}
	syncTicketLinkIndexes(t)
	t.UpdatedAt = time.Now().Unix()
	return *t, nil
}

// Delete removes a ticket.
func (m *ticketManager) Delete(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tickets {
		if m.tickets[i].ID == id {
			m.tickets = append(m.tickets[:i], m.tickets[i+1:]...)
			return
		}
	}
}

func (m *ticketManager) Get(id int64) (Ticket, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.find(id); t != nil {
		out := *t
		normalizeTicketKind(&out)
		return out, true
	}
	return Ticket{}, false
}

// List returns tickets newest-first. kindFilter empty = all.
func (m *ticketManager) List(kindFilter string) []Ticket {
	m.mu.Lock()
	defer m.mu.Unlock()
	kindFilter = strings.ToLower(strings.TrimSpace(kindFilter))
	out := make([]Ticket, 0, len(m.tickets))
	for _, t := range m.tickets {
		normalizeTicketKind(&t)
		if kindFilter != "" && t.Kind != kindFilter {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// TicketListRow is the list projection of a Ticket.
//
// A ticket inlines its whole comment thread, and every comment may carry up to
// maxAttachmentsPerComment attachments of ~2MB base64 each — so returning full
// tickets from the list endpoint makes one screen of work orders weigh tens or
// hundreds of megabytes on an install that has been running a year. Work orders
// are append-only business flow: nothing trims them, so the list can only get
// heavier. The list never renders the thread; detail comes from
// GET /api/v1/tickets/{id}.
type TicketListRow struct {
	Ticket
	CommentCount    int `json:"comment_count"`
	AttachmentCount int `json:"attachment_count"`
}

// ticketListRows drops the comment thread and attachments, keeping their counts
// so a list row can still say "3 comments" without shipping them.
func ticketListRows(list []Ticket) []TicketListRow {
	out := make([]TicketListRow, 0, len(list))
	for _, t := range list {
		atts := len(t.Attachments)
		for _, c := range t.Comments {
			atts += len(c.Attachments)
		}
		row := TicketListRow{CommentCount: len(t.Comments), AttachmentCount: atts}
		// t is a per-iteration copy; nil-ing here does not touch the manager.
		t.Comments, t.Attachments = nil, nil
		row.Ticket = t
		out = append(out, row)
	}
	return out
}

// OpenCount returns tickets that are not resolved/closed (for nav badges).
func (m *ticketManager) OpenCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for i := range m.tickets {
		if m.tickets[i].Status == "open" || m.tickets[i].Status == "in_progress" {
			n++
		}
	}
	return n
}

// Export/Import bridge the manager to the DB snapshot.
func (m *ticketManager) Export() []Ticket {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Ticket, len(m.tickets))
	copy(out, m.tickets)
	return out
}

func (m *ticketManager) Import(list []Ticket) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickets = make([]Ticket, len(list))
	copy(m.tickets, list)
	var maxID int64
	for i := range m.tickets {
		normalizeTicketKind(&m.tickets[i])
		syncTicketLinkIndexes(&m.tickets[i])
		if m.tickets[i].ID > maxID {
			maxID = m.tickets[i].ID
		}
	}
	m.nextID = maxID
}
