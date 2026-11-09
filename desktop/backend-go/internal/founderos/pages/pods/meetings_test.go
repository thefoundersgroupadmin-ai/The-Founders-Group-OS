package pods

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rhl/businessos-backend/internal/founderos/pages/catalogdb"
)

func TestBookedMeetingsAreRecordedOnceAndGetACallBrief(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	b := newFakeBoard()
	p := readyPod(t, s, "Brightvale")
	if _, err := s.Provision(ctx, b, p.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO founderos_funnel_contacts (id, workspace_id, name, status, venture, created_at, email, company)
		VALUES ('ct-p', $1, 'Priya Shah', 'first_touch', 'client', now(), 'priya@brightvale.example', 'Brightvale')`, p.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	list := []Meeting{
		{UID: "b-1", Title: "Discovery call", Start: soon, AttendeeEmail: "Priya@Brightvale.example", AttendeeName: "Priya Shah"},
		{UID: "b-2", Title: "Old", Start: time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339), AttendeeEmail: "x@y.example"},
		{UID: "b-3", Title: "Cancelled", Start: soon, AttendeeEmail: "z@y.example", Cancelled: true},
	}
	for i := 0; i < 2; i++ {
		if _, err := s.RecordMeetings(ctx, p.Slug, list); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM founderos_pod_meetings`).Scan(&n)
	if n != 2 {
		t.Fatalf("meetings recorded %d, want 2 (cancelled skipped, nothing twice)", n)
	}
	var status string
	_ = s.Pool.QueryRow(ctx, `SELECT status FROM founderos_funnel_contacts WHERE id = 'ct-p'`).Scan(&status)
	if status != "engaged" {
		t.Fatalf("booking did not stop the sequence: %s", status)
	}
	f := &fakeTasks{}
	k, err := s.TaskBriefs(ctx, f)
	if err != nil || k != 1 || len(f.bodies) != 1 {
		t.Fatalf("briefs tasked %d %v (only upcoming meetings)", k, err)
	}
	for _, want := range []string{"Priya Shah", "Discovery call", `"brief"`, "what to ask"} {
		if !strings.Contains(f.bodies[0], want) {
			t.Errorf("brief task lacks %q", want)
		}
	}
	if k, _ := s.TaskBriefs(ctx, f); k != 0 {
		t.Fatal("brief tasked twice")
	}
	// The agent's brief arrives; it waits to be sent to the rep.
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET brief = 'Priya runs ops at Brightvale...' WHERE booking_uid = 'b-1'`); err != nil {
		t.Fatal(err)
	}
	// A paused pod emails nothing; the brief waits (Codex M6 review #8).
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusPaused, "holiday"); err != nil {
		t.Fatal(err)
	}
	if held, _ := s.ClaimBriefs(ctx); len(held) != 0 {
		t.Fatal("paused pod emailed a brief")
	}
	if _, err := s.SetStatus(ctx, p.Slug, StatusDryRun, ""); err != nil {
		t.Fatal(err)
	}
	ready, err := s.ClaimBriefs(ctx)
	if err != nil || len(ready) != 1 || ready[0].Client != "Brightvale" {
		t.Fatalf("ready briefs %+v %v", ready, err)
	}
	if err := s.BriefDelivered(ctx, ready[0], "", false); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.ClaimBriefs(ctx); len(again) != 0 {
		t.Fatal("brief sent twice")
	}
}

// Codex M2-M4 review #6: reschedules move the meeting and re-send the brief;
// cancellations cancel it.
func TestRescheduledAndCancelledMeetingsAreReconciled(t *testing.T) {
	s := &Store{Pool: catalogdb.TestDB(t)}
	ctx := context.Background()
	p := newPod(t, s, "Brightvale")
	at := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	_, _ = s.RecordMeetings(ctx, p.Slug, []Meeting{{UID: "b-1", Title: "Call", Start: at.Format(time.RFC3339), AttendeeEmail: "a@b.example"}})
	if _, err := s.Pool.Exec(ctx, `UPDATE founderos_pod_meetings SET brief = 'x', brief_sent_at = now()`); err != nil {
		t.Fatal(err)
	}
	later := at.Add(24 * time.Hour)
	_, _ = s.RecordMeetings(ctx, p.Slug, []Meeting{{UID: "b-1", Title: "Call", Start: later.Format(time.RFC3339), AttendeeEmail: "a@b.example"}})
	var got time.Time
	var sent *time.Time
	_ = s.Pool.QueryRow(ctx, `SELECT starts_at, brief_sent_at FROM founderos_pod_meetings WHERE booking_uid = 'b-1'`).Scan(&got, &sent)
	if !got.Equal(later) || sent != nil {
		t.Fatalf("reschedule: %v sent=%v", got, sent)
	}
	_, _ = s.RecordMeetings(ctx, p.Slug, []Meeting{{UID: "b-1", Cancelled: true}})
	var status string
	_ = s.Pool.QueryRow(ctx, `SELECT status FROM founderos_pod_meetings WHERE booking_uid = 'b-1'`).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("cancellation: %s", status)
	}
}
