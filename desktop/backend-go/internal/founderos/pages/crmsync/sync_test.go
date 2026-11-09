package crmsync

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
	"github.com/rhl/businessos-backend/internal/founderos/pages/pods"
)

type fakeCRM struct {
	contacts map[string]Contact // remote id → contact
	emails   []string
	meetings []string
	notes    []string
}

func (f *fakeCRM) Name() string { return "fake" }
func (f *fakeCRM) UpsertContact(_ context.Context, c Contact) (string, error) {
	for id, have := range f.contacts {
		if have.Email == c.Email {
			return id, nil
		}
	}
	id := fmt.Sprintf("r-%d", len(f.contacts)+1)
	f.contacts[id] = c
	return id, nil
}
func (f *fakeCRM) LogEmail(_ context.Context, rid string, e Email) error {
	f.emails = append(f.emails, rid+":"+e.Subject)
	return nil
}
func (f *fakeCRM) LogMeeting(_ context.Context, rid string, m Meeting) error {
	f.meetings = append(f.meetings, rid+":"+m.Title+":"+m.Body)
	return nil
}
func (f *fakeCRM) AddNote(_ context.Context, rid, text string) error {
	f.notes = append(f.notes, rid+":"+text)
	return nil
}

func TestThePodsWorkReachesTheClientsCRMOnce(t *testing.T) {
	s := &pods.Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p, err := s.Create(ctx, pods.NewPod{ClientName: "Brightvale", OwnerID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.Create(ctx, pods.NewPod{ClientName: "Kestrel", OwnerID: "u1"})
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email, person, company, next_action)
		VALUES ('ct-p', $1, 'Priya Shah', 'engaged', 'client', now(), 'priya@brightvale.example', 'Priya Shah', 'Brightvale',
		'Qualified. Budget: £40k; Timing: Q1'),
		('ct-x', $2, 'Kim', 'engaged', 'client', now(), 'kim@kestrel.example', 'Kim', 'Kestrel', NULL)`, p.WorkspaceID, other.WorkspaceID)
	exec(`INSERT INTO founderos_outbox (id, workspace_id, source, to_address, subject, body, status, sent_at, contact_id)
		VALUES ('o-1', $1, 'paperclip', 'priya@brightvale.example', 'Three warehouses', 'Hi Priya', 'sent', now(), 'ct-p'),
		('o-2', $1, 'paperclip', 'priya@brightvale.example', 'Draft', 'x', 'pending', NULL, 'ct-p'),
		('o-3', $1, 'paperclip', 'priya@brightvale.example', 'Test', 'x', 'simulated', now(), 'ct-p')`, p.WorkspaceID)
	if _, err := s.RecordMeetings(ctx, p.Slug, []pods.Meeting{{UID: "b-1", Title: "Discovery call", Start: "2030-01-07T10:00:00Z",
		AttendeeEmail: "priya@brightvale.example", AttendeeName: "Priya Shah"}}); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE founderos_pod_meetings SET brief = 'Priya runs ops.'`)
	f := &fakeCRM{contacts: map[string]Contact{}}
	for i := 0; i < 2; i++ {
		if _, err := SyncPod(ctx, s.Pool, p.ID, f); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.contacts) != 1 {
		t.Fatalf("contacts %v (only this pod's, once)", f.contacts)
	}
	for _, c := range f.contacts {
		if c.FirstName != "Priya" || c.LastName != "Shah" || c.Company != "Brightvale" {
			t.Fatalf("contact %+v", c)
		}
	}
	if strings.Join(f.emails, ",") != "r-1:Three warehouses" {
		t.Fatalf("emails %v (sent only, once)", f.emails)
	}
	if len(f.meetings) != 1 || !strings.Contains(f.meetings[0], "Priya runs ops.") {
		t.Fatalf("meetings %v", f.meetings)
	}
	if len(f.notes) != 1 || !strings.Contains(f.notes[0], "Budget: £40k") {
		t.Fatalf("notes %v", f.notes)
	}
}

// Codex: CRM records were written once and never updated. Later changes now
// reach the CRM as notes, once each, and nothing is noted on the first pass.
func TestLaterChangesReachTheCRMAsNotesOnce(t *testing.T) {
	s := &pods.Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p, err := s.Create(ctx, pods.NewPod{ClientName: "Brightvale", OwnerID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email, person, company)
		VALUES ('ct-p', $1, 'Priya Shah', 'first_touch', 'client', now(), 'priya@brightvale.example', 'Priya Shah', 'Brightvale')`, p.WorkspaceID)
	if _, err := s.RecordMeetings(ctx, p.Slug, []pods.Meeting{{UID: "b-1", Title: "Discovery call", Start: "2030-01-07T10:00:00Z",
		AttendeeEmail: "priya@brightvale.example", AttendeeName: "Priya Shah"}}); err != nil {
		t.Fatal(err)
	}
	f := &fakeCRM{contacts: map[string]Contact{}}
	pass := func() {
		t.Helper()
		if _, err := SyncPod(ctx, s.Pool, p.ID, f); err != nil {
			t.Fatal(err)
		}
	}
	pass()
	if len(f.meetings) != 1 || len(f.notes) != 0 {
		t.Fatalf("first pass: meetings %v notes %v", f.meetings, f.notes)
	}
	// The brief arrives, the call moves, then is held with coaching; the
	// prospect then opts out.
	exec(`UPDATE founderos_pod_meetings SET brief = 'Priya runs ops.', starts_at = '2030-01-08T10:00:00Z'`)
	pass()
	pass()
	exec(`UPDATE founderos_pod_meetings SET outcome = 'held', coaching = 'Ask about budget sooner.'`)
	exec(`INSERT INTO founderos_email_suppression (workspace_id, address, reason) VALUES ($1, 'priya@brightvale.example', 'reply')`, p.WorkspaceID)
	pass()
	pass()
	all := strings.Join(f.notes, "\n")
	for _, want := range []string{"Call brief for Discovery call", "Priya runs ops.", "moved to Tue 8 Jan 2030", "was held", "Ask about budget sooner.", "Asked not to be contacted"} {
		if strings.Count(all, want) != 1 {
			t.Errorf("want %q once in notes:\n%s", want, all)
		}
	}
	if len(f.notes) != 5 {
		t.Fatalf("notes %d:\n%s", len(f.notes), all)
	}
	// Cancelling it is noted once.
	exec(`UPDATE founderos_pod_meetings SET status = 'cancelled'`)
	pass()
	pass()
	if !strings.Contains(f.notes[len(f.notes)-1], "was cancelled") || len(f.notes) != 6 || len(f.meetings) != 1 {
		t.Fatalf("after cancel: %v / %v", f.notes, f.meetings)
	}
}
